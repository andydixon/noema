package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Executor carries plans forward. It is the only planning component that
// asks the capability layer to act, and it does so only through
// capabilities.Service.Invoke and RequestCapability. It holds no plan state
// in memory: every tick reads the database, claims a step with a
// conditional update, acts, and records the outcome, so a restart resumes
// where it stopped and two executors cannot run the same step.
type Executor struct {
	Plans       *Store
	Volition    *volition.Store
	Caps        *capabilities.Service
	Predictions *predictions.Store
	Resolver    *predictions.Resolver
	Events      *evstore.Store
	Minds       *minds.Store
	Memory      *memory.Store
	Planner     *Planner
	Log         *slog.Logger
	Now         func() time.Time

	mu   sync.Mutex
	busy map[ids.ID]bool
	wake chan struct{}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) rec() volition.Recorder {
	return volition.Recorder{Q: e.Plans.db, Emit: volition.DirectEmitter(e.Events), Source: "planning"}
}

// Wake asks the run loop to tick soon.
func (e *Executor) Wake() {
	e.mu.Lock()
	if e.wake == nil {
		e.wake = make(chan struct{}, 1)
	}
	ch := e.wake
	e.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run ticks on wake-ups and every interval until ctx ends. It is started
// once by the scheduler; it starts no goroutines of its own.
func (e *Executor) Run(ctx context.Context, interval time.Duration) error {
	e.mu.Lock()
	if e.wake == nil {
		e.wake = make(chan struct{}, 1)
	}
	ch := e.wake
	e.mu.Unlock()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
		case <-t.C:
		}
		if err := e.Tick(ctx); err != nil && e.Log != nil {
			e.Log.Warn("plan executor tick failed", "err", err)
		}
	}
}

// Tick advances every active mind's open plans.
func (e *Executor) Tick(ctx context.Context) error {
	mindsWithPlans, err := e.Plans.ActiveMinds(ctx)
	if err != nil {
		return err
	}
	for _, m := range mindsWithPlans {
		if err := e.TickMind(ctx, m); err != nil && e.Log != nil {
			e.Log.Warn("plan execution failed", "mind", m, "err", err)
		}
	}
	return nil
}

// TickMind advances one mind's open plans until nothing more moves. Sandbox
// minds (disabled forks used by replay and experiments) are never advanced,
// and the forbidden context of dreaming or replay stops every action in the
// capability layer regardless.
func (e *Executor) TickMind(ctx context.Context, mind ids.ID) error {
	m, err := e.Minds.Get(ctx, mind)
	if err != nil {
		return err
	}
	if m.Status != minds.StatusActive {
		return nil
	}
	e.mu.Lock()
	if e.busy == nil {
		e.busy = map[ids.ID]bool{}
	}
	if e.busy[mind] {
		e.mu.Unlock()
		return nil
	}
	e.busy[mind] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.busy, mind); e.mu.Unlock() }()
	for round := 0; round < 20; round++ {
		plans, err := e.Plans.OpenPlans(ctx, mind)
		if err != nil {
			return err
		}
		moved := false
		for _, p := range plans {
			changed, err := e.advancePlan(ctx, p.ID)
			if err != nil && e.Log != nil {
				e.Log.Warn("advance plan", "plan", p.ID, "err", err)
			}
			moved = moved || changed
		}
		if !moved {
			return nil
		}
	}
	return nil
}

// Start plans an intention inside the caller's transaction: propose,
// validate, store and link. It acts on nothing; the executor picks the plan
// up after the transaction commits.
func (e *Executor) Start(ctx context.Context, r volition.Recorder, it volition.Intention) (Plan, error) {
	return e.start(ctx, r, it, false)
}

// start plans an intention. When fresh, earlier revisions do not exclude
// their strategies: the plan is being redone under new constraints (a
// commitment now requires approval), not because the strategy failed.
func (e *Executor) start(ctx context.Context, r volition.Recorder, it volition.Intention, fresh bool) (Plan, error) {
	previous, err := e.Plans.ForIntention(ctx, r.Q, it.ID)
	if err != nil {
		return Plan{}, err
	}
	considered := previous
	if fresh {
		considered = nil
	}
	d, err := e.Planner.Propose(ctx, it, considered)
	if err != nil {
		return Plan{}, err
	}
	v := Validate(&d, e.Planner.Registry)
	est := EstimateDraft(d, e.Planner.Registry)
	d.Estimate = &est
	var prev *Plan
	if len(previous) > 0 {
		prev = &previous[len(previous)-1]
	}
	p, err := e.Plans.Create(ctx, r, it, d, v, prev)
	if err != nil {
		return p, err
	}
	if err := e.Volition.SetPlan(ctx, r.Q, it.MindID, it.ID, p.ID); err != nil {
		return p, err
	}
	if !v.Valid {
		return p, fmt.Errorf("%w: %s", ErrInvalidPlan, strings.Join(v.Errors, "; "))
	}
	return p, nil
}

// advancePlan moves every movable step of one plan once and settles the
// plan's status. It reports whether anything changed.
func (e *Executor) advancePlan(ctx context.Context, id ids.ID) (bool, error) {
	p, err := e.Plans.Get(ctx, nil, id)
	if err != nil || !PlanOpen(p.Status) {
		return false, err
	}
	if p.Origin == "dream" {
		return false, nil
	}
	it, err := e.Volition.GetIntention(ctx, nil, p.MindID, p.IntentionID)
	if err != nil {
		return false, err
	}
	if !it.Status.Open() {
		return true, e.closePlan(ctx, p, PlanAbandoned, "its intention is "+string(it.Status))
	}
	if it.Status == volition.IntentionSuspended {
		return false, nil
	}
	byKey := map[string]*Step{}
	for i := range p.Steps {
		byKey[p.Steps[i].Key] = &p.Steps[i]
	}
	changed := false
	for i := range p.Steps {
		st := &p.Steps[i]
		moved, err := e.advanceStep(ctx, &p, it, st, byKey)
		if err != nil {
			return changed, err
		}
		if moved {
			// Re-read before touching anything else: a step change may have
			// unblocked, closed or replaced the plan.
			e.settle(ctx, p.ID)
			return true, nil
		}
	}
	return e.settle(ctx, p.ID) || changed, nil
}

func (e *Executor) advanceStep(ctx context.Context, p *Plan, it volition.Intention, st *Step, byKey map[string]*Step) (bool, error) {
	now := e.now()
	due := st.NextRunAt == nil || !st.NextRunAt.After(now)
	switch st.Status {
	case StepPending:
		for _, d := range st.DependsOn {
			dep := byKey[d]
			if dep == nil {
				continue
			}
			if dep.Status == StepFailed || dep.Status == StepCancelled {
				st.Status, st.StatusReason = StepCancelled, "dependency "+d+" did not complete"
				return e.Plans.SaveIf(ctx, *st, StepPending)
			}
			if !StepDone(dep.Status) {
				return false, nil
			}
		}
		st.Status = StepReady
		return e.Plans.SaveIf(ctx, *st, StepPending)
	case StepReady:
		if !due || it.Status != volition.IntentionActive {
			return false, nil
		}
		switch st.Kind {
		case KindCapability:
			return e.runCapability(ctx, p, it, st)
		case KindRequestCapability:
			return e.raiseRequest(ctx, p, it, st)
		case KindVerify, KindMonitor:
			if ok, err := e.Plans.Claim(ctx, st, StepReady, StepExecuting, time.Minute); err != nil || !ok {
				return false, err
			}
			e.stepEvent(ctx, *p, *st, events.PlanStepStarted, "started: "+st.Title, nil)
			return e.runCheck(ctx, p, it, st, byKey)
		case KindWait:
			next := now.Add(time.Duration(st.Spec.Wait.Seconds) * time.Second)
			st.Status, st.NextRunAt = StepWaiting, &next
			if ok, err := e.Plans.Claim(ctx, st, StepReady, StepWaiting, 0); err != nil || !ok {
				return false, err
			}
			st.NextRunAt = &next
			return true, e.Plans.Save(ctx, nil, *st)
		case KindInternal:
			if ok, err := e.Plans.Claim(ctx, st, StepReady, StepExecuting, time.Minute); err != nil || !ok {
				return false, err
			}
			return e.runInternal(ctx, p, it, st, byKey)
		}
	case StepWaiting:
		if !due {
			return false, nil
		}
		switch st.Kind {
		case KindWait:
			return e.complete(ctx, p, st, "waited", nil)
		case KindVerify, KindMonitor:
			if ok, err := e.Plans.Claim(ctx, st, StepWaiting, StepExecuting, time.Minute); err != nil || !ok {
				return false, err
			}
			return e.runCheck(ctx, p, it, st, byKey)
		}
	case StepAwaitingApproval:
		return e.pollApproval(ctx, p, it, st)
	case StepAwaitingGrant:
		return e.pollRequest(ctx, p, it, st)
	case StepExecuting:
		if st.LeaseUntil != nil && st.LeaseUntil.Before(now) {
			return e.recover(ctx, p, it, st)
		}
	}
	return false, nil
}

// ---- capability steps ----

func (e *Executor) runCapability(ctx context.Context, p *Plan, it volition.Intention, st *Step) (bool, error) {
	c, ok := e.Caps.Registry.Get(st.Capability)
	if !ok {
		return true, e.fail(ctx, p, st, "capability "+st.Capability+" is not registered", volition.TriggerCapabilityChanged, &volition.ActionInfo{Capability: st.Capability, Effect: "unknown"})
	}
	if exhausted := e.budgetExhausted(*p); exhausted != "" {
		st.Status, st.StatusReason = StepBlocked, "budget exhausted: "+exhausted
		if err := e.Plans.Save(ctx, nil, *st); err != nil {
			return false, err
		}
		return true, e.reconsider(ctx, *p, it, volition.Trigger{Kind: volition.TriggerResourceExhausted, Detail: exhausted})
	}
	if st.Precondition != nil {
		holds, obs, err := e.observe(ctx, p, st, st.Precondition.Check, "precondition")
		if err != nil {
			return false, nil // cannot observe yet; try on the next tick
		}
		if !holds {
			st.Status, st.Observation = StepSkipped, obs
			st.StatusReason = "precondition no longer holds: " + st.Precondition.Check.Description
			if err := e.Plans.Save(ctx, nil, *st); err != nil {
				return false, err
			}
			e.stepEvent(ctx, *p, *st, events.PlanStepCompleted, "skipped: "+st.Title+" — "+st.StatusReason, map[string]any{"skipped": true})
			return true, e.reconsider(ctx, *p, it, volition.Trigger{Kind: volition.TriggerWorldChanged, Detail: st.Precondition.Check.Description, OutcomeObserved: st.Precondition.SatisfiedIfFalse})
		}
	}
	if ok, err := e.Plans.Claim(ctx, st, StepReady, StepExecuting, c.Timeout+30*time.Second); err != nil || !ok {
		return false, err
	}
	e.stepEvent(ctx, *p, *st, events.PlanStepStarted, "started: "+st.Title, nil)
	inv := capabilities.Invocation{MindID: p.MindID, Capability: st.Capability, Target: st.Target, Params: st.Params, Origin: st.Origin(), Correlation: it.ID,
		// An intention's approval requirement (from a commitment) binds every
		// step that changes the world; reads it needs for diagnosis do not.
		RequireApproval: st.RequireApproval || (it.RequireApproval && c.Access == capabilities.AccessWrite), Reason: e.explain(*p, it, *st, c),
		Evidence: []capabilities.EvidenceRef{{Kind: "intention", ID: string(it.ID), Note: it.Description}, {Kind: "plan", ID: string(p.ID), Note: p.Objective}, {Kind: "plan_step", ID: string(st.ID), Note: st.Title}}}
	out, err := e.Caps.Invoke(ctx, inv)
	e.Plans.AddActual(ctx, p.ID, Resources{Invocations: 1, WallSeconds: float64(0)}) //nolint:errcheck
	switch {
	case errors.Is(err, capabilities.ErrForbiddenContext):
		st.Status, st.StatusReason, st.LeaseUntil = StepBlocked, "actions are forbidden in this context", nil
		return true, e.Plans.Save(ctx, nil, *st)
	case err != nil:
		return true, e.fail(ctx, p, st, err.Error(), volition.TriggerStepFailed, nil)
	}
	return e.adopt(ctx, p, it, st, c, out)
}

// explain is the reason recorded with an invocation and shown to an
// approver: what, why, expected outcome, side effects and alternatives.
func (e *Executor) explain(p Plan, it volition.Intention, st Step, c capabilities.Capability) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s. Intention: %s.", st.Title, it.Description)
	if st.Expectation != nil {
		fmt.Fprintf(&b, " Expected: %s.", st.Expectation.Description)
	}
	if c.SideEffects != "" {
		fmt.Fprintf(&b, " Side effects: %s.", c.SideEffects)
	}
	var alts []string
	for _, a := range p.Alternatives {
		if !a.Selected && a.Why != "already failed" {
			alts = append(alts, a.Strategy)
		}
	}
	if len(alts) > 0 {
		fmt.Fprintf(&b, " Alternatives considered: %s.", strings.Join(alts, ", "))
	}
	if st.RequireApproval || (it.RequireApproval && c.Access == capabilities.AccessWrite) {
		b.WriteString(" A commitment or value constraint requires approval for this action.")
	}
	return b.String()
}

// adopt records an Invoke outcome on the step.
func (e *Executor) adopt(ctx context.Context, p *Plan, it volition.Intention, st *Step, c capabilities.Capability, out capabilities.Outcome) (bool, error) {
	st.LeaseUntil = nil
	switch out.Status {
	case "pending":
		st.Status, st.ApprovalID, st.StatusReason = StepAwaitingApproval, out.ApprovalID, out.Decision.Reason
		if err := e.Plans.Save(ctx, nil, *st); err != nil {
			return false, err
		}
		e.Plans.AddActual(ctx, p.ID, Resources{Approvals: 1}) //nolint:errcheck
		return true, e.subordinate(ctx, p, it, st, volition.IntentRequestApproval, "obtain approval to "+st.Title, volition.BlockApproval, st.Capability+" requires operator approval ("+out.Decision.Reason+")")
	case "denied":
		kind := volition.TriggerCapabilityChanged
		effect := "deny"
		if out.Decision.Locked {
			effect = "locked"
		}
		return true, e.fail(ctx, p, st, "policy denied "+st.Capability+": "+out.Decision.Reason, kind, &volition.ActionInfo{Capability: st.Capability, Target: st.Target, Effect: effect})
	case "failed":
		if st.Attempts < st.MaxAttempts && c.Idempotent {
			next := e.now().Add(time.Duration(10*st.Attempts) * time.Second)
			st.Status, st.NextRunAt, st.Error, st.ExecutionID = StepReady, &next, out.Error, out.Execution
			return true, e.Plans.Save(ctx, nil, *st)
		}
		st.ExecutionID = out.Execution
		return true, e.fail(ctx, p, st, out.Error, volition.TriggerStepFailed, nil)
	case "executed":
		st.ExecutionID = out.Execution
		var data map[string]any
		if out.Result != nil {
			data = map[string]any{"summary": out.Result.Summary, "data": out.Result.Data}
		}
		return e.completeAction(ctx, p, it, st, data, out.EventID)
	}
	return true, e.fail(ctx, p, st, "unexpected outcome "+out.Status, volition.TriggerStepFailed, nil)
}

// completeAction completes a capability step and turns its expectation into
// a prediction that a verify step will resolve.
func (e *Executor) completeAction(ctx context.Context, p *Plan, it volition.Intention, st *Step, result map[string]any, cause ids.ID) (bool, error) {
	if st.Expectation != nil && st.PredictionID.IsZero() && e.Predictions != nil {
		x := st.Expectation
		pr, err := e.Predictions.Create(ctx, e.Plans.db, predictions.New{MindID: p.MindID, Statement: x.Description, Subject: st.Target, Confidence: x.Confidence, WindowEnd: e.now().Add(time.Duration(x.WithinSeconds) * time.Second),
			Criteria: predictions.Criteria{Kind: "verification"}, Source: "planning", OriginEvent: cause, BeliefID: x.BeliefID})
		if err != nil {
			return false, err
		}
		st.PredictionID = pr.ID
		e.Events.Append(ctx, events.New{MindID: p.MindID, Type: events.PredictionCreated, Source: "planning", Summary: fmt.Sprintf("expects (%.2f) after %s: %s", pr.Confidence, st.Title, pr.Statement), Salience: 0.4, Confidence: pr.Confidence, //nolint:errcheck
			CausationID: cause, CorrelationID: it.ID, Evidence: []events.EvidenceRef{{Kind: "prediction", ID: string(pr.ID)}, {Kind: "plan_step", ID: string(st.ID)}},
			Metadata: map[string]any{"prediction_id": string(pr.ID), "plan_id": string(p.ID), "step_id": string(st.ID), "window_end": pr.WindowEnd, "belief_id": string(x.BeliefID)}})
	}
	return e.complete(ctx, p, st, "completed: "+st.Title, result)
}

func (e *Executor) complete(ctx context.Context, p *Plan, st *Step, summary string, result map[string]any) (bool, error) {
	st.Status, st.LeaseUntil, st.NextRunAt, st.Error = StepCompleted, nil, nil, ""
	if result != nil {
		st.Result = result
	}
	if err := e.Plans.Save(ctx, nil, *st); err != nil {
		return false, err
	}
	e.stepEvent(ctx, *p, *st, events.PlanStepCompleted, summary, map[string]any{"execution_id": string(st.ExecutionID), "prediction_id": string(st.PredictionID)})
	return true, nil
}

// ---- approvals, requests and subordinate intentions ----

// subordinate forms the subordinate intention a waiting step implies (obtain
// approval, obtain access) and blocks the parent with the reason: the
// intention–action gap made visible.
func (e *Executor) subordinate(ctx context.Context, p *Plan, it volition.Intention, st *Step, kind volition.IntentionKind, description, blockKind, blockReason string) error {
	r := e.rec()
	if st.IntentionID.IsZero() {
		sub, err := e.Volition.CreateIntention(ctx, r, volition.Intention{MindID: p.MindID, ParentID: it.ID, Description: description, DesiredOutcome: "authority to continue " + it.Description, Kind: kind,
			SourceWants: it.SourceWants, GoalID: it.GoalID, Priority: it.Priority, Confidence: 0.5, PlanID: p.ID, RequiredCapabilities: []string{st.Capability},
			Formation: map[string]any{"formed_by": "plan step " + st.Key, "reason": blockReason}})
		if err != nil {
			return err
		}
		st.IntentionID = sub.ID
		if err := e.Plans.Save(ctx, nil, *st); err != nil {
			return err
		}
	}
	if it.Status == volition.IntentionActive {
		_, err := e.Volition.Apply(ctx, r, p.MindID, it.ID, volition.Reconsideration{Outcome: volition.Block, BlockedKind: blockKind, Reason: blockReason}, volition.Trigger{Kind: "awaiting_authority", Detail: st.Title})
		return err
	}
	return nil
}

func (e *Executor) resumeParent(ctx context.Context, p *Plan, it volition.Intention, st *Step, reason string) error {
	r := e.rec()
	if !st.IntentionID.IsZero() {
		if sub, err := e.Volition.GetIntention(ctx, nil, p.MindID, st.IntentionID); err == nil && sub.Status.Open() {
			if _, err := e.Volition.Apply(ctx, r, p.MindID, sub.ID, volition.Reconsideration{Outcome: volition.Fulfil, Reason: reason}, volition.Trigger{Kind: volition.TriggerRequestGranted}); err != nil {
				return err
			}
		}
	}
	cur, err := e.Volition.GetIntention(ctx, nil, p.MindID, it.ID)
	if err == nil && cur.Status == volition.IntentionBlocked {
		_, err = e.Volition.Apply(ctx, r, p.MindID, it.ID, volition.Reconsideration{Outcome: volition.Resume, Reason: reason}, volition.Trigger{Kind: volition.TriggerRequestGranted})
	}
	return err
}

func (e *Executor) dropSubordinate(ctx context.Context, p *Plan, st *Step, reason string) {
	if st.IntentionID.IsZero() {
		return
	}
	if sub, err := e.Volition.GetIntention(ctx, nil, p.MindID, st.IntentionID); err == nil && sub.Status.Open() {
		e.Volition.Apply(ctx, e.rec(), p.MindID, sub.ID, volition.Reconsideration{Outcome: volition.Abandon, Reason: reason}, volition.Trigger{Kind: volition.TriggerApprovalDenied}) //nolint:errcheck
	}
}

func (e *Executor) pollApproval(ctx context.Context, p *Plan, it volition.Intention, st *Step) (bool, error) {
	a, err := e.Caps.GetApproval(ctx, st.ApprovalID)
	if err != nil {
		return false, err
	}
	switch a.Status {
	case "pending":
		if e.now().After(a.ExpiresAt) {
			e.Caps.ExpirePending(ctx) //nolint:errcheck
			return e.pollApproval(ctx, p, it, st)
		}
		return false, nil
	case "approved":
		return false, nil // execution in progress in the capability layer
	case "executed", "failed":
		if err := e.resumeParent(ctx, p, it, st, "operator "+a.DecisionBy+" approved"); err != nil {
			return false, err
		}
		cause := a.EventID
		execs, _ := e.Caps.ExecutionsByOrigin(ctx, p.MindID, st.Origin())
		for _, x := range execs {
			if x.ApprovalID == a.ID {
				st.ExecutionID, cause = x.ID, x.EventID // the expectation follows from the execution, not the request
			}
		}
		if a.Status == "failed" {
			return true, e.fail(ctx, p, st, "approved action failed: "+a.Error, volition.TriggerStepFailed, nil)
		}
		var res map[string]any
		if len(a.Result) > 0 {
			json.Unmarshal(a.Result, &res) //nolint:errcheck
		}
		it, _ = e.Volition.GetIntention(ctx, nil, p.MindID, it.ID)
		return e.completeAction(ctx, p, it, st, res, cause)
	case "denied":
		e.dropSubordinate(ctx, p, st, "operator denied the approval")
		return true, e.fail(ctx, p, st, "operator "+a.DecisionBy+" denied: "+a.DecisionNote, volition.TriggerApprovalDenied, nil)
	case "expired", "withdrawn":
		e.dropSubordinate(ctx, p, st, "approval "+a.Status)
		return true, e.fail(ctx, p, st, "approval "+a.Status+" before a decision", volition.TriggerApprovalExpired, nil)
	}
	return false, nil
}

func (e *Executor) raiseRequest(ctx context.Context, p *Plan, it volition.Intention, st *Step) (bool, error) {
	spec := st.Spec.Request
	if spec == nil {
		return true, e.fail(ctx, p, st, "request step without a request", volition.TriggerStepFailed, nil)
	}
	// If a live grant already covers the need, nothing needs asking.
	if grants, err := e.Caps.ScopedGrants(ctx, p.MindID, true); err == nil {
		for _, g := range grants {
			if g.Capability == spec.Capability && (g.TargetPattern == spec.TargetPattern || g.TargetPattern == "*") {
				return e.complete(ctx, p, st, "access already granted: "+st.Title, map[string]any{"grant_id": g.ID})
			}
		}
	}
	if ok, err := e.Plans.Claim(ctx, st, StepReady, StepExecuting, 2*time.Minute); err != nil || !ok {
		return false, err
	}
	var evidence []capabilities.EvidenceRef
	for _, s := range p.Steps {
		if s.Status == StepCompleted && !s.ExecutionID.IsZero() {
			evidence = append(evidence, capabilities.EvidenceRef{Kind: "execution", ID: string(s.ExecutionID), Note: s.Title})
		}
	}
	req, err := e.Caps.RequestCapability(ctx, capabilities.CapabilityRequest{MindID: p.MindID, Capability: spec.Capability, TargetPattern: spec.TargetPattern, ParamConstraints: spec.ParamConstraints, DurationSeconds: spec.DurationSeconds,
		MaxOperations: spec.MaxOperations, Reason: spec.Reason, Risk: spec.Risk, Mitigation: spec.Mitigation, Fallback: spec.Fallback, Alternatives: spec.Alternatives, Evidence: evidence, IntentionID: it.ID, PlanID: p.ID, StepID: st.ID})
	switch {
	case errors.Is(err, capabilities.ErrAttentionBudget):
		next := e.now().Add(10 * time.Minute)
		st.Status, st.NextRunAt, st.LeaseUntil, st.StatusReason = StepReady, &next, nil, "operator attention budget in use; asking again later"
		return true, e.Plans.Save(ctx, nil, *st)
	case errors.Is(err, capabilities.ErrForbiddenContext):
		st.Status, st.StatusReason, st.LeaseUntil = StepBlocked, "requests are forbidden in this context", nil
		return true, e.Plans.Save(ctx, nil, *st)
	case err != nil:
		return true, e.fail(ctx, p, st, "request refused: "+err.Error(), volition.TriggerStepFailed, nil)
	}
	st.Status, st.RequestID, st.LeaseUntil, st.StatusReason = StepAwaitingGrant, req.ID, nil, "waiting for an operator to decide the request"
	if err := e.Plans.Save(ctx, nil, *st); err != nil {
		return false, err
	}
	e.Plans.AddActual(ctx, p.ID, Resources{Approvals: 1}) //nolint:errcheck
	st.Capability = spec.Capability
	return true, e.subordinate(ctx, p, it, st, volition.IntentRequestCapability, "obtain temporary "+spec.Capability+" access to "+spec.TargetPattern, volition.BlockCapability, spec.Capability+" needs authority this mind does not hold")
}

func (e *Executor) pollRequest(ctx context.Context, p *Plan, it volition.Intention, st *Step) (bool, error) {
	req, err := e.Caps.GetCapabilityRequest(ctx, st.RequestID)
	if err != nil {
		return false, err
	}
	switch req.Status {
	case "pending":
		if e.now().After(req.ExpiresAt) {
			e.Caps.ExpireCapabilityRequests(ctx) //nolint:errcheck
			return e.pollRequest(ctx, p, it, st)
		}
		return false, nil
	case "granted":
		if err := e.resumeParent(ctx, p, it, st, "operator "+req.DecidedBy+" granted "+req.Capability); err != nil {
			return false, err
		}
		return e.complete(ctx, p, st, "granted: "+st.Title, map[string]any{"grant_id": req.GrantID, "decided_by": req.DecidedBy})
	case "denied":
		e.dropSubordinate(ctx, p, st, "operator denied the request")
		return true, e.fail(ctx, p, st, "operator "+req.DecidedBy+" denied the request: "+req.DecisionNote, volition.TriggerApprovalDenied, nil)
	default:
		e.dropSubordinate(ctx, p, st, "request "+req.Status)
		return true, e.fail(ctx, p, st, "request "+req.Status, volition.TriggerApprovalExpired, nil)
	}
}

// ---- observation ----

// observe runs a read-only check through the capability layer.
func (e *Executor) observe(ctx context.Context, p *Plan, st *Step, ck Check, purpose string) (bool, map[string]any, error) {
	// A check never raises an approval request: if policy would not simply
	// allow the observation, it is reported as not observable.
	if d, err := e.Caps.Preview(ctx, capabilities.Invocation{MindID: p.MindID, Capability: ck.Capability, Target: ck.Target, Params: ck.Params}); err != nil || d.Effect != capabilities.Allow {
		reason := d.Reason
		if err != nil {
			reason = err.Error()
		}
		return false, map[string]any{"status": "not_observable", "reason": reason}, fmt.Errorf("cannot observe with %s: %s", ck.Capability, reason)
	}
	out, err := e.Caps.Invoke(ctx, capabilities.Invocation{MindID: p.MindID, Capability: ck.Capability, Target: ck.Target, Params: ck.Params, Origin: st.Origin() + ":" + purpose,
		Reason: purpose + " for " + st.Title + ": " + ck.Description, Correlation: p.IntentionID})
	e.Plans.AddActual(ctx, p.ID, Resources{Invocations: 1}) //nolint:errcheck
	if err != nil {
		return false, nil, err
	}
	if out.Status != "executed" || out.Result == nil {
		return false, map[string]any{"status": out.Status, "reason": out.Decision.Reason, "error": out.Error}, fmt.Errorf("observation %s: %s%s", out.Status, out.Decision.Reason, out.Error)
	}
	holds, val, err := Evaluate(ck, out.Result.Data)
	obs := map[string]any{"capability": ck.Capability, "path": ck.Path, "op": ck.Op, "expected": ck.Value, "observed": val, "holds": holds, "execution_id": out.Execution, "at": e.now()}
	return holds, obs, err
}

func (e *Executor) runCheck(ctx context.Context, p *Plan, it volition.Intention, st *Step, byKey map[string]*Step) (bool, error) {
	now := e.now()
	switch st.Kind {
	case KindVerify:
		spec := st.Spec.Verify
		of := byKey[spec.Of]
		if of == nil {
			return true, e.fail(ctx, p, st, "verified step missing", volition.TriggerStepFailed, nil)
		}
		acted := of.UpdatedAt
		if of.FinishedAt != nil {
			acted = *of.FinishedAt
		}
		deadline := acted.Add(time.Duration(spec.WithinSeconds) * time.Second)
		holds, obs, obsErr := e.observe(ctx, p, st, spec.Check, "verify")
		st.Observation = obs
		if obsErr == nil && holds {
			if err := e.resolveExpectation(ctx, p, of, predictions.StatusConfirmed, "observed: "+spec.Check.Description, obs); err != nil {
				return false, err
			}
			return e.complete(ctx, p, st, "verified: "+spec.Check.Description, obs)
		}
		if now.Before(deadline) {
			next := now.Add(time.Duration(spec.IntervalSeconds) * time.Second)
			st.Status, st.NextRunAt, st.LeaseUntil = StepWaiting, &next, nil
			if obsErr != nil {
				st.StatusReason = "could not observe yet: " + obsErr.Error()
			} else {
				st.StatusReason = fmt.Sprintf("not yet: %s is %v; checking again until %s", spec.Check.Path, obs["observed"], deadline.Format(time.RFC3339))
			}
			return true, e.Plans.Save(ctx, nil, *st)
		}
		if obsErr != nil {
			// Unknown is not refuted: nothing was observed, so the expectation expires.
			if err := e.resolveExpectation(ctx, p, of, predictions.StatusExpired, "could not observe the outcome: "+obsErr.Error(), obs); err != nil {
				return false, err
			}
			return true, e.fail(ctx, p, st, "could not verify "+spec.Check.Description+": "+obsErr.Error(), volition.TriggerStepFailed, nil)
		}
		reason := fmt.Sprintf("expected %s, observed %s %s %v", spec.Check.Description, spec.Check.Path, "=", obs["observed"])
		if err := e.resolveExpectation(ctx, p, of, predictions.StatusRefuted, reason, obs); err != nil {
			return false, err
		}
		return true, e.fail(ctx, p, st, "the action did not have its expected outcome: "+reason, volition.TriggerExpectationFailed, nil)
	case KindMonitor:
		spec := st.Spec.Monitor
		started := now
		if st.StartedAt != nil {
			started = *st.StartedAt
		}
		holds, obs, obsErr := e.observe(ctx, p, st, spec.Check, "monitor")
		state := st.Result
		if state == nil {
			state = map[string]any{}
		}
		if obsErr == nil && holds {
			since, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(state["stable_since"]))
			if since.IsZero() {
				since = now
				state["stable_since"] = now.Format(time.RFC3339Nano)
			}
			if now.Sub(since) >= time.Duration(spec.StableSeconds)*time.Second {
				st.Observation = obs
				return e.complete(ctx, p, st, fmt.Sprintf("stable for %s: %s", now.Sub(since).Truncate(time.Second), spec.Check.Description), state)
			}
		} else {
			delete(state, "stable_since")
		}
		st.Result, st.Observation = state, obs
		if now.Sub(started) >= time.Duration(spec.DeadlineSeconds)*time.Second {
			return true, e.fail(ctx, p, st, "did not stay stable before the deadline: "+spec.Check.Description, volition.TriggerExpectationFailed, nil)
		}
		next := now.Add(time.Duration(spec.IntervalSeconds) * time.Second)
		st.Status, st.NextRunAt, st.LeaseUntil = StepWaiting, &next, nil
		return true, e.Plans.Save(ctx, nil, *st)
	}
	return false, nil
}

func (e *Executor) resolveExpectation(ctx context.Context, p *Plan, of *Step, status predictions.Status, reason string, obs map[string]any) error {
	if of.PredictionID.IsZero() || e.Resolver == nil {
		return nil
	}
	pr, err := e.Predictions.Get(ctx, p.MindID, of.PredictionID)
	if err != nil || pr.Status != predictions.StatusOpen {
		return nil
	}
	var refs []events.EvidenceRef
	if x, ok := obs["execution_id"].(ids.ID); ok && !x.IsZero() {
		refs = append(refs, events.EvidenceRef{Kind: "execution", ID: string(x), Note: "observation"})
	}
	_, evs, err := e.Resolver.ResolveIn(ctx, e.Plans.db, pr, predictions.Outcome{Status: status, Reason: reason, Evidence: refs}, "planning.verify")
	if err != nil {
		return err
	}
	e.Events.Publish(evs...)
	return nil
}

// ---- internal steps ----

func (e *Executor) runInternal(ctx context.Context, p *Plan, it volition.Intention, st *Step, byKey map[string]*Step) (bool, error) {
	var lines []string
	var refs []events.EvidenceRef
	for _, d := range st.DependsOn {
		dep := byKey[d]
		if dep == nil {
			continue
		}
		summary := fmt.Sprint(dep.Result["summary"])
		if dep.Result == nil || summary == "<nil>" {
			summary = dep.StatusReason
		}
		lines = append(lines, fmt.Sprintf("%s (%s): %s", dep.Title, dep.Status, summary))
		if !dep.ExecutionID.IsZero() {
			refs = append(refs, events.EvidenceRef{Kind: "execution", ID: string(dep.ExecutionID), Note: dep.Title})
		}
	}
	text := "Findings for " + p.Objective + ":\n- " + strings.Join(lines, "\n- ")
	// The findings re-enter cognition as an observation from Noema's own
	// instrumentation, where attention, memory and belief formation apply.
	evs, err := e.Events.Append(ctx, events.New{MindID: p.MindID, Type: events.Observation, Source: "planning.findings", Summary: trunc("findings: "+strings.Join(lines, "; "), 400), Salience: 0.7, Confidence: 0.9,
		CorrelationID: it.ID, Evidence: append(refs, events.EvidenceRef{Kind: "plan", ID: string(p.ID)}), Metadata: map[string]any{"plan_id": string(p.ID), "intention_id": string(it.ID), "trust": events.TrustSystem},
		Payload: events.ObservationPayload{Text: text, ContentType: "text/plain", Source: "planning", Trust: events.TrustSystem, Method: "plan", Reference: string(p.ID)}})
	if err != nil {
		return false, err
	}
	return e.complete(ctx, p, st, "summarised findings of "+fmt.Sprint(len(lines))+" steps", map[string]any{"summary": text, "observation_event": evs[0].ID})
}

// ---- failure, reconsideration and replanning ----

func (e *Executor) fail(ctx context.Context, p *Plan, st *Step, reason, trigger string, action *volition.ActionInfo) error {
	st.Status, st.Error, st.LeaseUntil, st.NextRunAt = StepFailed, reason, nil, nil
	if err := e.Plans.Save(ctx, nil, *st); err != nil {
		return err
	}
	e.stepEvent(ctx, *p, *st, events.PlanStepFailed, "failed: "+st.Title+" — "+reason, map[string]any{"trigger": trigger})
	it, err := e.Volition.GetIntention(ctx, nil, p.MindID, p.IntentionID)
	if err != nil {
		return err
	}
	return e.reconsider(ctx, *p, it, volition.Trigger{Kind: trigger, Detail: reason, Action: action})
}

// reconsider applies the volition rule for a trigger and carries out its
// consequence for the plan: replan into a new revision, abandon, suspend or
// block. The old plan is kept and linked, never rewritten.
func (e *Executor) reconsider(ctx context.Context, p Plan, it volition.Intention, t volition.Trigger) error {
	if !it.Status.Open() {
		return e.closePlan(ctx, p, PlanAbandoned, "its intention is "+string(it.Status))
	}
	r := e.rec()
	rc := volition.Reconsider(it, t, volition.ReconsiderContext{Replans: p.Revision - 1, MaxReplans: p.MaxReplans, Now: e.now()})
	switch rc.Outcome {
	case volition.Replan, volition.Tighten:
		if won, err := e.Plans.BeginReplan(ctx, p.ID, rc.Reason); err != nil || !won {
			return err // another reconsideration is already replanning or has closed this plan
		}
		cur, err := e.Volition.Apply(ctx, r, p.MindID, it.ID, rc, t)
		if err != nil {
			return err
		}
		e.cancelOpenSteps(ctx, p, "replanning: "+rc.Reason)
		np, err := e.start(ctx, r, cur, rc.Outcome == volition.Tighten)
		if err != nil {
			reason := "no alternative plan remains: " + err.Error()
			if _, aerr := e.Volition.Apply(ctx, r, p.MindID, it.ID, volition.Reconsideration{Outcome: volition.Abandon, Reason: reason}, volition.Trigger{Kind: volition.TriggerReplanLimit, Detail: err.Error()}); aerr != nil && !errors.Is(aerr, volition.ErrClosed) {
				return aerr
			}
			if np.ID.IsZero() {
				return e.closePlan(ctx, p, PlanFailed, reason)
			}
			return e.closePlan(ctx, np, PlanFailed, reason)
		}
		return nil
	case volition.Abandon:
		if _, err := e.Volition.Apply(ctx, r, p.MindID, it.ID, rc, t); err != nil && !errors.Is(err, volition.ErrClosed) {
			return err
		}
		if t.Kind == volition.TriggerPlanCompleted {
			return e.closePlan(ctx, p, PlanCompleted, rc.Reason)
		}
		return e.closePlan(ctx, p, PlanAbandoned, rc.Reason)
	case volition.Fulfil:
		if _, err := e.Volition.Apply(ctx, r, p.MindID, it.ID, rc, t); err != nil && !errors.Is(err, volition.ErrClosed) {
			return err
		}
		return e.closePlan(ctx, p, PlanCompleted, rc.Reason)
	case volition.Keep:
		return nil
	case volition.Suspend, volition.Block:
		if _, err := e.Volition.Apply(ctx, r, p.MindID, it.ID, rc, t); err != nil {
			return err
		}
		return e.Plans.SetStatus(ctx, nil, p.ID, PlanBlocked, rc.Reason)
	}
	return nil
}

func (e *Executor) cancelOpenSteps(ctx context.Context, p Plan, reason string) {
	steps, _ := e.Plans.Steps(ctx, nil, p.ID)
	for _, s := range steps {
		if StepTerminal(s.Status) {
			continue
		}
		if s.Status == StepAwaitingApproval && !s.ApprovalID.IsZero() {
			e.Caps.WithdrawApproval(ctx, p.MindID, s.ApprovalID, reason) //nolint:errcheck
		}
		if s.Status == StepAwaitingGrant && !s.RequestID.IsZero() {
			e.Caps.WithdrawCapabilityRequest(ctx, p.MindID, s.RequestID, reason) //nolint:errcheck
		}
		e.dropSubordinate(ctx, &p, &s, reason)
		from := s.Status
		s.Status, s.StatusReason = StepCancelled, reason
		e.Plans.SaveIf(ctx, s, from) //nolint:errcheck // a step that moved on meanwhile keeps its newer state
	}
}

// Achieves reports whether completing a plan of this strategy means the
// intention's desired outcome was reached. An escalation completes without it.
func Achieves(strategy string) bool { return strategy != "diagnose_then_escalate" }

// Reconsider applies a trigger (for example a weakened assumption or a new
// commitment) to an open plan from outside the executor.
func (e *Executor) Reconsider(ctx context.Context, plan ids.ID, t volition.Trigger) error {
	p, err := e.Plans.Get(ctx, nil, plan)
	if err != nil {
		return err
	}
	if !PlanOpen(p.Status) {
		return nil
	}
	it, err := e.Volition.GetIntention(ctx, nil, p.MindID, p.IntentionID)
	if err != nil {
		return err
	}
	return e.reconsider(ctx, p, it, t)
}

// Cancel abandons a plan on behalf of cognition or an operator.
func (e *Executor) Cancel(ctx context.Context, plan ids.ID, reason string) error {
	p, err := e.Plans.Get(ctx, nil, plan)
	if err != nil {
		return err
	}
	if !PlanOpen(p.Status) && p.Status != PlanDraft {
		return nil
	}
	return e.closePlan(ctx, p, PlanAbandoned, reason)
}

func (e *Executor) closePlan(ctx context.Context, p Plan, status, reason string) error {
	cur, err := e.Plans.Get(ctx, nil, p.ID)
	if err != nil {
		return err
	}
	if !PlanOpen(cur.Status) && cur.Status != PlanDraft {
		return nil
	}
	e.cancelOpenSteps(ctx, cur, reason)
	if err := e.Plans.SetStatus(ctx, nil, p.ID, status, reason); err != nil {
		return err
	}
	typ := map[string]events.Type{PlanCompleted: events.PlanCompleted, PlanAbandoned: events.PlanAbandoned, PlanFailed: events.PlanAbandoned}[status]
	if typ != "" {
		e.Events.Append(ctx, events.New{MindID: p.MindID, Type: typ, Source: "planning", Summary: fmt.Sprintf("plan %s: %s — %s", status, p.Objective, reason), Salience: 0.5, Confidence: 1, CorrelationID: p.IntentionID, //nolint:errcheck
			Evidence: []events.EvidenceRef{{Kind: "plan", ID: string(p.ID)}, {Kind: "intention", ID: string(p.IntentionID)}}, Metadata: map[string]any{"plan_id": string(p.ID), "intention_id": string(p.IntentionID), "status": status, "revision": p.Revision}})
	}
	if status == PlanCompleted {
		e.recordProcedure(ctx, cur)
	}
	return nil
}

// settle derives the plan status from its steps and handles completion.
func (e *Executor) settle(ctx context.Context, id ids.ID) bool {
	p, err := e.Plans.Get(ctx, nil, id)
	if err != nil || !PlanOpen(p.Status) || p.Status == PlanReplanning {
		return false
	}
	counts := map[string]int{}
	for _, s := range p.Steps {
		counts[s.Status]++
	}
	status := p.Status
	switch {
	case counts[StepCompleted]+counts[StepSkipped] == len(p.Steps):
		it, err := e.Volition.GetIntention(ctx, nil, p.MindID, p.IntentionID)
		if err == nil {
			e.reconsider(ctx, p, it, volition.Trigger{Kind: volition.TriggerPlanCompleted, Detail: p.CompletionCriteria, OutcomeObserved: Achieves(p.Strategy)}) //nolint:errcheck
		}
		return true
	case counts[StepExecuting] > 0:
		status = PlanExecuting
	case counts[StepAwaitingApproval]+counts[StepAwaitingGrant] > 0:
		status = PlanAwaitingApproval
	case counts[StepBlocked] > 0:
		status = PlanBlocked
	case counts[StepWaiting] > 0 && counts[StepReady] == 0:
		status = PlanWaiting
	case counts[StepReady]+counts[StepPending] > 0 && p.Status != PlanValidated:
		status = PlanExecuting
	}
	if status != p.Status {
		e.Plans.SetStatus(ctx, nil, p.ID, status, "") //nolint:errcheck
		return true
	}
	return false
}

// recover handles a step whose lease expired while executing: the process
// stopped mid-action. What already happened is looked up by origin; a
// non-idempotent action with no record is not repeated.
func (e *Executor) recover(ctx context.Context, p *Plan, it volition.Intention, st *Step) (bool, error) {
	if st.Kind != KindCapability {
		st.Status, st.LeaseUntil, st.StatusReason = StepReady, nil, "resumed after interruption"
		if st.Kind == KindVerify || st.Kind == KindMonitor {
			st.Status = StepWaiting
		}
		return true, e.Plans.Save(ctx, nil, *st)
	}
	execs, err := e.Caps.ExecutionsByOrigin(ctx, p.MindID, st.Origin())
	if err != nil {
		return false, err
	}
	for i := len(execs) - 1; i >= 0; i-- {
		x := execs[i]
		if st.StartedAt != nil && x.CreatedAt.Before(st.StartedAt.Add(-time.Second)) {
			continue
		}
		st.ExecutionID, st.StatusReason = x.ID, "outcome recovered after interruption"
		if x.Error != "" {
			return true, e.fail(ctx, p, st, "recovered failed execution: "+x.Error, volition.TriggerStepFailed, nil)
		}
		var res map[string]any
		json.Unmarshal(x.Result, &res) //nolint:errcheck
		return e.completeAction(ctx, p, it, st, res, x.EventID)
	}
	approvals, err := e.Caps.ApprovalsByOrigin(ctx, p.MindID, st.Origin())
	if err != nil {
		return false, err
	}
	if n := len(approvals); n > 0 {
		st.Status, st.ApprovalID, st.LeaseUntil, st.StatusReason = StepAwaitingApproval, approvals[n-1].ID, nil, "approval request recovered after interruption"
		return true, e.Plans.Save(ctx, nil, *st)
	}
	c, _ := e.Caps.Registry.Get(st.Capability)
	if c.Idempotent || c.Access == capabilities.AccessRead {
		st.Status, st.LeaseUntil, st.StatusReason = StepReady, nil, "interrupted before any record; safe to repeat"
		return true, e.Plans.Save(ctx, nil, *st)
	}
	st.Status, st.LeaseUntil = StepBlocked, nil
	st.StatusReason = "interrupted while running " + st.Capability + " with no record of the outcome; not repeating a non-idempotent action without verification"
	if err := e.Plans.Save(ctx, nil, *st); err != nil {
		return false, err
	}
	if it.Status == volition.IntentionActive {
		if _, err := e.Volition.Apply(ctx, e.rec(), p.MindID, it.ID, volition.Reconsideration{Outcome: volition.Block, BlockedKind: volition.BlockVerify, Reason: st.StatusReason}, volition.Trigger{Kind: "interrupted"}); err != nil {
			return false, err
		}
	}
	return true, e.Plans.SetStatus(ctx, nil, p.ID, PlanBlocked, st.StatusReason)
}

// RetryStep lets an operator release a blocked step (for example after
// checking that an interrupted action did not run).
func (e *Executor) RetryStep(ctx context.Context, step ids.ID, by string) error {
	st, err := e.Plans.GetStep(ctx, step)
	if err != nil {
		return err
	}
	if st.Status != StepBlocked {
		return fmt.Errorf("%w: step is %s, not blocked", ErrInvalidPlan, st.Status)
	}
	st.Status, st.StatusReason = StepReady, "released by operator "+by
	if err := e.Plans.Save(ctx, nil, st); err != nil {
		return err
	}
	p, err := e.Plans.Get(ctx, nil, st.PlanID)
	if err != nil {
		return err
	}
	if it, err := e.Volition.GetIntention(ctx, nil, p.MindID, p.IntentionID); err == nil && it.Status == volition.IntentionBlocked {
		e.Volition.Apply(ctx, e.rec(), p.MindID, it.ID, volition.Reconsideration{Outcome: volition.Resume, Reason: "operator " + by + " released the blocked step"}, volition.Trigger{Kind: volition.TriggerOperator}) //nolint:errcheck
	}
	e.Plans.SetStatus(ctx, nil, p.ID, PlanExecuting, "") //nolint:errcheck
	e.Wake()
	return nil
}

func (e *Executor) budgetExhausted(p Plan) string {
	cur, _ := e.Plans.Get(context.Background(), nil, p.ID)
	b, a := cur.Budget, cur.Actual
	if b.MaxInvocations <= 0 {
		b.MaxInvocations = 20
	}
	switch {
	case a.Invocations >= b.MaxInvocations:
		return fmt.Sprintf("%d of %d capability invocations used", a.Invocations, b.MaxInvocations)
	case b.MaxApprovals > 0 && a.Approvals >= b.MaxApprovals:
		return fmt.Sprintf("%d of %d operator approvals used", a.Approvals, b.MaxApprovals)
	case b.MaxMoney > 0 && a.Money >= b.MaxMoney:
		return fmt.Sprintf("%.2f of %.2f spent", a.Money, b.MaxMoney)
	case b.MaxWallSeconds > 0 && e.now().Sub(cur.CreatedAt).Seconds() >= b.MaxWallSeconds:
		return fmt.Sprintf("wall time %.0fs exceeded", b.MaxWallSeconds)
	}
	return ""
}

// recordProcedure stores what worked as a procedural memory: the steps that
// completed and the observation that confirmed the outcome.
func (e *Executor) recordProcedure(ctx context.Context, p Plan) {
	if e.Memory == nil {
		return
	}
	var parts []string
	verified := false
	for _, s := range p.Steps {
		if s.Status != StepCompleted {
			continue
		}
		parts = append(parts, s.Title)
		if s.Kind == KindVerify || s.Kind == KindMonitor {
			verified = true
		}
	}
	if len(parts) == 0 {
		return
	}
	content := fmt.Sprintf("To %s: %s.", p.Objective, strings.Join(parts, "; then "))
	if verified {
		content += " The outcome was confirmed by observation."
	}
	if p.Revision > 1 {
		content += fmt.Sprintf(" This worked on revision %d, after earlier plans failed.", p.Revision)
	}
	evs, _ := e.Events.Query(ctx, evstore.Filter{MindID: p.MindID, Types: []events.Type{events.PlanCompleted}, Limit: 1})
	var origin ids.ID
	if len(evs) > 0 {
		origin = evs[0].ID
	}
	m, err := e.Memory.Create(ctx, e.Plans.db, memory.New{MindID: p.MindID, Kind: concepts.Procedural, Epistemic: concepts.Observation, Content: content, Source: "planning", Trust: events.TrustSystem, Confidence: map[bool]float64{true: 0.8, false: 0.6}[verified],
		Importance: 0.6, Salience: 0.5, OccurredAt: e.now(), OriginEvent: origin, Detail: map[string]any{"plan_id": p.ID, "intention_id": p.IntentionID, "strategy": p.Strategy, "revision": p.Revision, "verified": verified}})
	if err == nil {
		e.Events.Append(ctx, events.New{MindID: p.MindID, Type: events.MemoryCreated, Source: "planning", Summary: "learned a procedure: " + trunc(content, 150), Salience: 0.4, Confidence: m.Confidence, CausationID: origin, //nolint:errcheck
			Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(m.ID)}, {Kind: "plan", ID: string(p.ID)}}, Metadata: map[string]any{"memory_id": m.ID, "kind": m.Kind, "plan_id": string(p.ID)}})
	}
}

func (e *Executor) stepEvent(ctx context.Context, p Plan, st Step, typ events.Type, summary string, extra map[string]any) {
	meta := map[string]any{"plan_id": string(p.ID), "step_id": string(st.ID), "step_key": st.Key, "intention_id": string(p.IntentionID), "kind": st.Kind, "capability": st.Capability, "status": st.Status}
	for k, v := range extra {
		meta[k] = v
	}
	evidence := []events.EvidenceRef{{Kind: "plan_step", ID: string(st.ID)}, {Kind: "plan", ID: string(p.ID)}, {Kind: "intention", ID: string(p.IntentionID)}}
	if !st.ExecutionID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "execution", ID: string(st.ExecutionID)})
	}
	e.Events.Append(ctx, events.New{MindID: p.MindID, Type: typ, Source: "planning", Summary: trunc(summary, 300), Salience: 0.35, Confidence: 1, CorrelationID: p.IntentionID, Evidence: evidence, Metadata: meta, //nolint:errcheck
		Payload: map[string]any{"step": st}})
}
