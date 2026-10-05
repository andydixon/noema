package planning

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/capabilities/host"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// fakeHost stands in for systemd: a unit that restarts, and may or may not
// come back healthy.
type fakeHost struct {
	mu           sync.Mutex
	active       bool
	fixOnRestart bool
	restarts     int
}

func (f *fakeHost) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case name == "systemctl" && args[0] == "restart":
		f.restarts++
		if f.fixOnRestart {
			f.active = true
		}
		return nil, nil
	case name == "systemctl" && args[0] == "show":
		state := "failed"
		if f.active {
			state = "active"
		}
		return []byte("LoadState=loaded\nActiveState=" + state + "\nSubState=" + state + "\n"), nil
	case name == "journalctl":
		return []byte("07:14 atlas-worker: queue consumer lost connection to db-primary\n"), nil
	}
	return nil, fmt.Errorf("unexpected %s", name)
}

func (f *fakeHost) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.restarts }

type fixture struct {
	t      *testing.T
	ctx    context.Context
	mind   minds.Mind
	es     *evstore.Store
	caps   *capabilities.Service
	vol    *volition.Store
	plans  *Store
	bel    *beliefs.Store
	mem    *memory.Store
	exec   *Executor
	host   *fakeHost
	offset time.Duration
	mu     sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, err := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "planner"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, mind: m, host: &fakeHost{}}
	f.es = evstore.NewStore(pool, nil, nil)
	reg := capabilities.NewRegistry()
	for _, c := range capabilities.Builtins() {
		reg.Add(c)
	}
	for _, c := range host.Capabilities(host.Options{Runner: f.host}) {
		reg.Add(c)
	}
	reg.Freeze()
	log := slog.New(slog.DiscardHandler)
	f.caps = &capabilities.Service{Registry: reg, DB: pool, Events: f.es, Log: log}
	f.vol = volition.NewStore(pool)
	f.plans = NewStore(pool)
	f.bel = beliefs.NewStore(pool)
	f.mem = memory.NewStore(ctx, pool, nil)
	ps := predictions.NewStore(pool)
	planner := &Planner{Registry: reg, Effect: func(ctx context.Context, mind ids.ID, capability, target string, params map[string]any) string {
		d, err := f.caps.Preview(ctx, capabilities.Invocation{MindID: mind, Capability: capability, Target: target, Params: params})
		if err != nil {
			return "deny"
		}
		return string(d.Effect)
	}}
	f.exec = f.newExecutor(ps, planner, log)
	return f
}

func (f *fixture) newExecutor(ps *predictions.Store, planner *Planner, log *slog.Logger) *Executor {
	return &Executor{Plans: f.plans, Volition: f.vol, Caps: f.caps, Predictions: ps, Resolver: &predictions.Resolver{Store: ps, Events: f.es, Beliefs: f.bel, Log: log}, Events: f.es,
		Minds: minds.NewStore(f.plans.db), Memory: f.mem, Planner: planner, Log: log, Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return time.Now().Add(f.offset) }}
}

func (f *fixture) advance(d time.Duration) { f.mu.Lock(); f.offset += d; f.mu.Unlock() }

func (f *fixture) intention(requireApproval bool, beliefIDs ...ids.ID) volition.Intention {
	f.t.Helper()
	it, err := f.vol.CreateIntention(f.ctx, f.vol.Direct(f.es, "test"), volition.Intention{MindID: f.mind.ID, Description: "restart atlas-worker", Kind: volition.IntentAct, RequireApproval: requireApproval,
		Proposal: &volition.Proposal{Kind: "capability", Capability: "service.restart", Target: "atlas-worker", Params: map[string]any{"service": "atlas-worker"}}, Confidence: 0.7, Priority: 0.8, BeliefIDs: beliefIDs})
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func (f *fixture) start(it volition.Intention) Plan {
	f.t.Helper()
	p, err := f.exec.Start(f.ctx, volition.Recorder{Q: f.plans.db, Emit: volition.DirectEmitter(f.es), Source: "test"}, it)
	if err != nil {
		f.t.Fatalf("start: %v", err)
	}
	return p
}

func (f *fixture) tick() {
	f.t.Helper()
	if err := f.exec.TickMind(f.ctx, f.mind.ID); err != nil {
		f.t.Fatalf("tick: %v", err)
	}
}

func (f *fixture) allow(capability string) {
	if _, err := f.caps.SetPolicy(f.ctx, capabilities.Policy{MindID: f.mind.ID, Capability: capability, Effect: capabilities.Allow, CreatedBy: "andy"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) plan(id ids.ID) Plan {
	p, err := f.plans.Get(f.ctx, nil, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) step(p Plan, key string) Step {
	for _, s := range f.plan(p.ID).Steps {
		if s.Key == key {
			return s
		}
	}
	f.t.Fatalf("no step %s", key)
	return Step{}
}

func (f *fixture) intentionNow(id ids.ID) volition.Intention {
	it, err := f.vol.GetIntention(f.ctx, nil, f.mind.ID, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func (f *fixture) latestPlan(it ids.ID) Plan {
	ps, _ := f.plans.ForIntention(f.ctx, nil, it)
	return f.plan(ps[len(ps)-1].ID)
}

func (f *fixture) eventsOf(types ...events.Type) []events.Event {
	evs, _ := f.es.Query(f.ctx, evstore.Filter{MindID: f.mind.ID, Types: types, Limit: 500})
	return evs
}

func TestApprovedActionIsVerifiedAndLearnt(t *testing.T) {
	f := newFixture(t)
	f.host.fixOnRestart = true
	it := f.intention(true)
	p := f.start(it)
	if p.Status != PlanValidated || p.Strategy != "direct" || len(p.Steps) != 2 || !p.Steps[0].RequireApproval {
		t.Fatalf("plan: %+v", p)
	}
	f.tick()
	p1 := f.step(p, "P1")
	if p1.Status != StepAwaitingApproval || p1.ApprovalID.IsZero() || p1.IntentionID.IsZero() || f.host.count() != 0 {
		t.Fatalf("expected to wait for approval without acting: %+v restarts=%d", p1, f.host.count())
	}
	parent := f.intentionNow(it.ID)
	sub := f.intentionNow(p1.IntentionID)
	if parent.Status != volition.IntentionBlocked || parent.BlockedKind != volition.BlockApproval || sub.ParentID != it.ID || sub.Kind != volition.IntentRequestApproval {
		t.Fatalf("intention–action gap not represented: parent %+v sub %+v", parent, sub)
	}
	a, _ := f.caps.GetApproval(f.ctx, p1.ApprovalID)
	if !strings.Contains(a.Reason, "Expected:") || !strings.Contains(a.Reason, "Side effects:") || !strings.Contains(a.Reason, "requires approval for this action") || a.Origin != p1.Origin() {
		t.Fatalf("approval does not explain itself: %q origin %q", a.Reason, a.Origin)
	}
	f.tick() // nothing moves while the operator has not decided
	if f.host.count() != 0 {
		t.Fatal("acted before approval")
	}
	if _, err := f.caps.Approve(f.ctx, p1.ApprovalID, "andy", "go ahead", false, nil); err != nil {
		t.Fatal(err)
	}
	f.tick()
	p = f.plan(p.ID)
	if p.Status != PlanCompleted || f.host.count() != 1 {
		t.Fatalf("plan should complete after verification: %s restarts=%d steps=%+v", p.Status, f.host.count(), p.Steps)
	}
	if got := f.intentionNow(it.ID); got.Status != volition.IntentionFulfilled {
		t.Fatalf("intention: %+v", got)
	}
	if got := f.intentionNow(p1.IntentionID); got.Status != volition.IntentionFulfilled {
		t.Fatalf("subordinate: %+v", got)
	}
	p1 = f.step(p, "P1")
	pr, _ := f.exec.Predictions.Get(f.ctx, f.mind.ID, p1.PredictionID)
	if pr.Status != predictions.StatusConfirmed || p1.ExecutionID.IsZero() {
		t.Fatalf("expectation: %+v step %+v", pr, p1)
	}
	mems, _ := f.mem.List(f.ctx, f.mind.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Procedural}})
	if len(mems) != 1 || !strings.Contains(mems[0].Content, "confirmed by observation") {
		t.Fatalf("procedure not learnt: %+v", mems)
	}
}

func TestFailedExpectationReplansWithoutRepeatingTheAction(t *testing.T) {
	f := newFixture(t)
	f.allow("service.restart")
	res, err := f.bel.Create(f.ctx, f.plans.db, func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		evs, err := f.es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		return evs[0], nil
	}, beliefs.New{MindID: f.mind.ID, Proposition: "restarting atlas-worker clears the stalled queue", Epistemic: concepts.Hypothesis, Source: "test",
		Evidence: []beliefs.EvidenceInput{{Direction: "for", Kind: "note", Source: "operator:andy", Weight: 0.6, Credibility: 0.9, Derivation: beliefs.DerivationOperator, Family: "operator:andy"}}})
	if err != nil {
		t.Fatal(err)
	}
	before := res.Belief.Confidence
	it := f.intention(false, res.Belief.ID)
	p := f.start(it)
	f.tick()
	if f.host.count() != 1 || f.step(p, "V1").Status != StepWaiting {
		t.Fatalf("restart then wait: restarts=%d v1=%+v", f.host.count(), f.step(p, "V1"))
	}
	f.advance(61 * time.Second)
	f.tick()
	if got := f.plan(p.ID); got.Status != PlanSuperseded || got.SupersededBy.IsZero() {
		t.Fatalf("failed plan should be kept as superseded history: %+v", got)
	}
	if len(f.eventsOf(events.PredictionError)) != 1 || len(f.eventsOf(events.PlanReplanned)) != 1 {
		t.Fatal("prediction error or replan not recorded")
	}
	b, _ := f.bel.Get(f.ctx, nil, f.mind.ID, res.Belief.ID)
	if b.Confidence >= before {
		t.Fatalf("belief the expectation rested on did not weaken: %.3f → %.3f", before, b.Confidence)
	}
	p2 := f.latestPlan(it.ID)
	if p2.Revision != 2 || p2.Strategy != "diagnose_then_escalate" || p2.Supersedes != p.ID {
		t.Fatalf("second revision: %+v", p2)
	}
	f.tick()
	r1 := f.step(p2, "R1")
	if r1.Status != StepAwaitingGrant || r1.RequestID.IsZero() {
		t.Fatalf("log access should be requested: %+v", r1)
	}
	req, _ := f.caps.GetCapabilityRequest(f.ctx, r1.RequestID)
	if !strings.Contains(req.Risk, "personal") || req.Fallback == "" || req.IntentionID != it.ID {
		t.Fatalf("request lacks risk, fallback or provenance: %+v", req)
	}
	if _, err := f.caps.DecideCapabilityRequest(f.ctx, r1.RequestID, "andy", true, "read only", capabilities.Narrowing{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f.tick()
	}
	p2 = f.plan(p2.ID)
	if p2.Status != PlanCompleted {
		t.Fatalf("escalation plan: %s %+v", p2.Status, p2.Steps)
	}
	if f.host.count() != 1 {
		t.Fatalf("the failed restart was repeated: %d restarts", f.host.count())
	}
	if got := f.intentionNow(it.ID); got.Status != volition.IntentionAbandoned || !strings.Contains(got.StatusReason, "without achieving") {
		t.Fatalf("an escalation must not count as fulfilment: %+v", got)
	}
	if len(f.eventsOf(events.Observation)) == 0 {
		t.Fatal("findings did not re-enter cognition as an observation")
	}
}

func TestReplanLimitAndDenial(t *testing.T) {
	f := newFixture(t)
	it := f.intention(true)
	p := f.start(it)
	f.tick()
	a := f.step(p, "P1").ApprovalID
	if _, err := f.caps.Deny(f.ctx, a, "andy", "not during trading hours"); err != nil {
		t.Fatal(err)
	}
	f.tick()
	p2 := f.latestPlan(it.ID)
	if p2.Strategy != "diagnose_then_escalate" || f.host.count() != 0 {
		t.Fatalf("denial should revise, not act: %+v restarts=%d", p2, f.host.count())
	}
	// Deny the log request too: no strategy remains, so the intention is abandoned.
	f.tick()
	r1 := f.step(p2, "R1")
	if _, err := f.caps.DecideCapabilityRequest(f.ctx, r1.RequestID, "andy", false, "no", capabilities.Narrowing{}); err != nil {
		t.Fatal(err)
	}
	f.tick()
	got := f.intentionNow(it.ID)
	if got.Status != volition.IntentionAbandoned {
		t.Fatalf("intention after exhausting strategies: %+v", got)
	}
	ws := f.eventsOf(events.IntentionAbandoned)
	if len(ws) == 0 {
		t.Fatal("abandonment not recorded")
	}
}

func TestApprovalExpiryAndCapabilityRemovalMidPlan(t *testing.T) {
	f := newFixture(t)
	it := f.intention(true)
	p := f.start(it)
	f.tick()
	a := f.step(p, "P1").ApprovalID
	if _, err := f.plans.db.Exec(f.ctx, `UPDATE approval_requests SET expires_at=now()-interval '1 minute' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.latestPlan(it.ID).Revision != 2 {
		t.Fatal("expired approval did not lead to a revised plan")
	}

	g := newFixture(t)
	g.allow("service.restart")
	it2 := g.intention(false)
	p2 := g.start(it2)
	if _, err := g.caps.SetPolicy(g.ctx, capabilities.Policy{MindID: g.mind.ID, Capability: "service.restart", TargetPattern: "atlas-*", Effect: capabilities.Deny, CreatedBy: "andy"}); err != nil {
		t.Fatal(err)
	}
	g.tick()
	if g.host.count() != 0 || g.step(p2, "P1").Status != StepFailed {
		t.Fatalf("capability removed mid-plan still ran: %d %+v", g.host.count(), g.step(p2, "P1"))
	}
	if next := g.latestPlan(it2.ID); next.Revision != 2 {
		t.Fatalf("no revision after capability removal: %+v", next)
	}
}

func TestRestartDuringExecutionNeverDuplicatesAnAction(t *testing.T) {
	f := newFixture(t)
	f.allow("service.restart")
	f.host.fixOnRestart = true
	it := f.intention(false)
	p := f.start(it)
	p1 := f.step(p, "P1")
	// The process died after claiming the step and before recording anything.
	if _, err := f.plans.db.Exec(f.ctx, `UPDATE plan_steps SET status='executing', attempts=1, started_at=now(), lease_until=now()-interval '1 second' WHERE id=$1`, p1.ID); err != nil {
		t.Fatal(err)
	}
	f.tick()
	p1 = f.step(p, "P1")
	if p1.Status != StepBlocked || f.host.count() != 0 {
		t.Fatalf("non-idempotent action repeated blindly: %+v restarts=%d", p1, f.host.count())
	}
	if got := f.intentionNow(it.ID); got.Status != volition.IntentionBlocked || got.BlockedKind != volition.BlockVerify {
		t.Fatalf("intention should wait for verification: %+v", got)
	}
	if err := f.exec.RetryStep(f.ctx, p1.ID, "andy"); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.host.count() != 1 || f.plan(p.ID).Status != PlanCompleted {
		t.Fatalf("after release: restarts=%d status=%s", f.host.count(), f.plan(p.ID).Status)
	}

	// The process died after the action ran: the record is adopted, not repeated.
	g := newFixture(t)
	g.allow("service.restart")
	g.host.fixOnRestart = true
	it2 := g.intention(false)
	pl := g.start(it2)
	s := g.step(pl, "P1")
	if out, err := g.caps.Invoke(g.ctx, capabilities.Invocation{MindID: g.mind.ID, Capability: "service.restart", Params: map[string]any{"service": "atlas-worker"}, Origin: s.Origin()}); err != nil || out.Status != "executed" {
		t.Fatalf("simulated run: %v %+v", err, out)
	}
	if _, err := g.plans.db.Exec(g.ctx, `UPDATE plan_steps SET status='executing', attempts=1, started_at=now()-interval '5 seconds', lease_until=now()-interval '1 second' WHERE id=$1`, s.ID); err != nil {
		t.Fatal(err)
	}
	g.tick()
	if g.host.count() != 1 || g.plan(pl.ID).Status != PlanCompleted {
		t.Fatalf("recovered outcome: restarts=%d status=%s", g.host.count(), g.plan(pl.ID).Status)
	}
}

func TestConcurrentExecutorsRunAStepOnce(t *testing.T) {
	f := newFixture(t)
	f.allow("service.restart")
	f.host.fixOnRestart = true
	it := f.intention(false)
	p := f.start(it)
	// Many executors race over the same plan from its first transition, each
	// ticking repeatedly, as duplicate schedulers or several daemons would.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		e := f.newExecutor(f.exec.Predictions, f.exec.Planner, f.exec.Log)
		wg.Add(1)
		go func(e *Executor) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				e.TickMind(f.ctx, f.mind.ID) //nolint:errcheck
			}
		}(e)
	}
	wg.Wait()
	f.tick()
	if f.host.count() != 1 {
		t.Fatalf("duplicate scheduler execution: %d restarts", f.host.count())
	}
	execs, _ := f.caps.ExecutionsByOrigin(f.ctx, f.mind.ID, f.step(p, "P1").Origin())
	if len(execs) != 1 {
		t.Fatalf("executions recorded: %d", len(execs))
	}
}

func TestBudgetPreconditionDreamAndSandbox(t *testing.T) {
	f := newFixture(t)
	f.allow("service.restart")
	it := f.intention(false)
	p := f.start(it)
	if _, err := f.plans.db.Exec(f.ctx, `UPDATE plans SET budget='{"max_capability_invocations":1}', actual='{"capability_invocations":1}' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.host.count() != 0 || f.intentionNow(it.ID).Status != volition.IntentionSuspended || f.plan(p.ID).Status != PlanBlocked {
		t.Fatalf("budget exhausted: restarts=%d intention=%s plan=%s", f.host.count(), f.intentionNow(it.ID).Status, f.plan(p.ID).Status)
	}

	// Target state changed externally: already healthy, so the restart is skipped and the intention fulfilled.
	g := newFixture(t)
	g.allow("service.restart")
	g.host.active = true
	it2 := g.intention(false)
	d := Draft{Objective: "restore atlas-worker", Origin: "template", Strategy: "direct", Steps: []StepSpec{{Key: "P1", Title: "restart atlas-worker", Kind: KindCapability, Capability: "service.restart", Params: map[string]any{"service": "atlas-worker"},
		Precondition: &Precondition{SatisfiedIfFalse: true, Check: Check{Capability: "service.status", Params: map[string]any{"service": "atlas-worker"}, Path: "active", Op: "eq", Value: false, Description: "atlas-worker is still down"}}}}}
	v := Validate(&d, g.caps.Registry)
	pl, err := g.plans.Create(g.ctx, g.exec.rec(), it2, d, v, nil)
	if err != nil || !v.Valid {
		t.Fatalf("%v %+v", err, v)
	}
	g.tick()
	if g.host.count() != 0 || g.step(pl, "P1").Status != StepSkipped || g.intentionNow(it2.ID).Status != volition.IntentionFulfilled {
		t.Fatalf("world change: restarts=%d step=%+v intention=%s", g.host.count(), g.step(pl, "P1"), g.intentionNow(it2.ID).Status)
	}

	// A dream's plan and a disabled sandbox mind never act.
	h := newFixture(t)
	h.allow("service.restart")
	it3 := h.intention(false)
	d3 := Draft{Objective: "dreamt fix", Origin: "dream", Strategy: "dream", Steps: []StepSpec{{Key: "P1", Title: "restart", Kind: KindCapability, Capability: "service.restart", Params: map[string]any{"service": "atlas-worker"}}}}
	v3 := Validate(&d3, h.caps.Registry)
	dp, _ := h.plans.Create(h.ctx, h.exec.rec(), it3, d3, v3, nil)
	h.tick()
	if dp.Status != PlanDraft || h.host.count() != 0 {
		t.Fatalf("dream plan: %s restarts=%d", dp.Status, h.host.count())
	}
	h.start(it3)
	if _, err := h.plans.db.Exec(h.ctx, `UPDATE minds SET status='disabled' WHERE id=$1`, h.mind.ID); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if h.host.count() != 0 {
		t.Fatal("a disabled mind acted")
	}
}

func TestInvalidModelPlanNeverRuns(t *testing.T) {
	f := newFixture(t)
	f.exec.Planner.Model = func(context.Context, ids.ID, string, []capabilities.Capability) (string, ids.ID, error) {
		return `{"steps":[{"key":"P1","title":"become root","kind":"capability","capability":"shell.exec","params":{"cmd":"sudo -i"},"status":"completed"}]}`, ids.Nil, nil
	}
	it, _ := f.vol.CreateIntention(f.ctx, f.vol.Direct(f.es, "test"), volition.Intention{MindID: f.mind.ID, Description: "make the dashboard faster", Kind: volition.IntentPursue})
	_, err := f.exec.Start(f.ctx, f.exec.rec(), it)
	if err == nil || !strings.Contains(err.Error(), "shell.exec") {
		t.Fatalf("invalid model plan accepted: %v", err)
	}
	p := f.latestPlan(it.ID)
	if p.Status != PlanDraft || p.Validation.Valid || len(p.Validation.Warnings) == 0 {
		t.Fatalf("invalid plan should stay a draft with its errors and ignored fields: %+v", p)
	}
	f.tick()
	if execs, _ := f.caps.Executions(f.ctx, f.mind.ID, 10); len(execs) != 0 {
		t.Fatal("invalid plan executed")
	}
}

// A model is consulted only when no deterministic strategy applies (B2):
// planning a direct capability action must not spend a live model call on
// an alternative that cannot be selected.
func TestModelIsNotConsultedWhenADeterministicStrategyApplies(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.exec.Planner.Model = func(context.Context, ids.ID, string, []capabilities.Capability) (string, ids.ID, error) {
		calls++
		return `{"steps":[]}`, ids.Nil, nil
	}
	it, _ := f.vol.CreateIntention(f.ctx, f.vol.Direct(f.es, "test"), volition.Intention{MindID: f.mind.ID, Description: "check the status page", Kind: volition.IntentAct,
		Proposal: &volition.Proposal{Kind: "capability", Capability: "service.status", Params: map[string]any{"service": "atlas-worker"}}})
	p, err := f.exec.Start(f.ctx, f.exec.rec(), it)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("the model was called %d time(s) although the direct strategy applied", calls)
	}
	recorded := false
	for _, a := range p.Alternatives {
		recorded = recorded || (a.Strategy == "model" && !a.Selected && strings.HasPrefix(a.Why, "not consulted"))
	}
	if p.Strategy != "direct" || !recorded {
		t.Fatalf("expected the direct strategy with the model recorded as not consulted: %+v", p.Alternatives)
	}
	// With no deterministic strategy the model is consulted.
	pursue, _ := f.vol.CreateIntention(f.ctx, f.vol.Direct(f.es, "test"), volition.Intention{MindID: f.mind.ID, Description: "make the dashboard faster", Kind: volition.IntentPursue})
	f.exec.Start(f.ctx, f.exec.rec(), pursue) //nolint:errcheck
	if calls != 1 {
		t.Fatalf("the model should be consulted once when nothing else applies, got %d", calls)
	}
}

// Regression: an executor holding a stale "pending" copy of a step must not
// move it back to "ready" after another executor has claimed it, or the
// step can be claimed and run a second time.
func TestStaleStepCopyCannotMoveStepBackwards(t *testing.T) {
	f := newFixture(t)
	f.allow("service.restart")
	it := f.intention(false)
	p := f.start(it)
	stale := f.step(p, "P1") // pending
	fresh := stale
	fresh.Status = StepReady
	if ok, err := f.plans.SaveIf(f.ctx, fresh, StepPending); err != nil || !ok {
		t.Fatalf("pending → ready: %v %v", ok, err)
	}
	if ok, err := f.plans.Claim(f.ctx, &fresh, StepReady, StepExecuting, time.Minute); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	byKey := map[string]*Step{}
	for i := range p.Steps {
		byKey[p.Steps[i].Key] = &p.Steps[i]
	}
	if _, err := f.exec.advanceStep(f.ctx, &p, it, &stale, byKey); err != nil {
		t.Fatal(err)
	}
	if got := f.step(p, "P1"); got.Status != StepExecuting {
		t.Fatalf("stale copy overwrote a claimed step: %s", got.Status)
	}
}

// Two reconsiderations holding the same plan (an event reaction and an
// executor tick) must replan it once, not twice.
func TestConcurrentReconsiderationReplansOnce(t *testing.T) {
	f := newFixture(t)
	f.allow("service.restart")
	it := f.intention(false)
	p := f.start(it)
	trig := volition.Trigger{Kind: volition.TriggerStepFailed, Detail: "test"}
	if err := f.exec.reconsider(f.ctx, p, it, trig); err != nil {
		t.Fatal(err)
	}
	if err := f.exec.reconsider(f.ctx, p, it, trig); err != nil { // stale copies of the plan and intention
		t.Fatal(err)
	}
	if ps, _ := f.plans.ForIntention(f.ctx, nil, it.ID); len(ps) != 2 {
		t.Fatalf("want one replan, got %d plans", len(ps))
	}
}
