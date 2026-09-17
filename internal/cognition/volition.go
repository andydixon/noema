package cognition

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// volitionSource is the event source for volitional changes made inside a
// cycle. It deliberately does not start with "brain": a new want or
// intention is a change the next cycle should consider.
const volitionSource = "volition"

func recorder(c *brain.Cycle) volition.Recorder {
	return volition.Recorder{Q: c.Tx, Source: volitionSource, Now: c.State.Now, Correlation: c.State.CycleID,
		Emit: func(ctx context.Context, _ storage.Querier, n events.New) (events.Event, error) {
			return c.EmitNow(ctx, n)
		}}
}

func deriveContext(ctx context.Context, s *brain.State) volition.DeriveContext {
	d := volition.DeriveContext{Now: s.Now, Drives: map[string]float64{}, Values: s.Mind.Personality.Values, Goals: map[ids.ID]volition.GoalInfo{}, Commitments: map[ids.ID]volition.Commitment{}}
	for _, dr := range s.Motiv.Drives {
		d.Drives[dr.Name] = dr.Value
	}
	if s.Services.Goals != nil {
		if gs, err := s.Services.Goals.List(ctx, s.Mind.ID, nil, 300); err == nil {
			for _, g := range gs {
				d.Goals[g.ID] = volition.GoalInfo{ID: g.ID, Title: g.Title, Subject: g.Subject, Kind: string(g.Kind), Owner: g.Owner, Priority: g.Priority, DueAt: g.DueAt}
			}
		}
	}
	if s.Services.Volition != nil {
		if cs, err := s.Services.Volition.Commitments(ctx, nil, s.Mind.ID, []string{"active"}, 500); err == nil {
			for _, c := range cs {
				d.Commitments[c.ID] = c
			}
		}
	}
	return d
}

// ActionInfo describes a proposed capability action for volition: its
// metadata from the registry and the effect policy would give it now.
func ActionInfo(ctx context.Context, caps *capabilities.Service, mind ids.ID, p *volition.Proposal) *volition.ActionInfo {
	if p == nil || p.Kind != "capability" || caps == nil {
		return nil
	}
	a := &volition.ActionInfo{Capability: p.Capability, Target: p.Target, Effect: "unknown"}
	c, ok := caps.Registry.Get(p.Capability)
	if !ok {
		return a
	}
	a.Access, a.Risk, a.Idempotent, a.SideEffects, a.WallSeconds = string(c.Access), string(c.Risk), c.Idempotent, c.SideEffects, c.Estimate.WallSeconds
	if c.Locked {
		a.Effect = "locked"
		return a
	}
	target := p.Target
	if c.TargetFrom != nil {
		target = ""
	}
	d, err := caps.Preview(ctx, capabilities.Invocation{MindID: mind, Capability: p.Capability, Target: target, Params: p.Params})
	if err != nil {
		a.Effect = "deny"
		return a
	}
	if t, err := c.ResolveTarget("", p.Params); err == nil && t != "" {
		a.Target = t
	}
	a.Effect = string(d.Effect)
	return a
}

// ---- after-cycle hook: wants and commitments ----

// VolitionHook derives wants from what the cycle perceived and produced,
// satisfies answer wants when a reply was sent, raises due, overdue,
// expired and conflicting commitments, and lets faded wants expire.
type VolitionHook struct{}

// Name implements brain.Hook.
func (VolitionHook) Name() string { return "volition" }

// AfterCycle implements brain.Hook.
func (VolitionHook) AfterCycle(ctx context.Context, c *brain.Cycle, emitted []events.Event) error {
	vs := c.State.Services.Volition
	if vs == nil {
		return nil
	}
	r := recorder(c)
	mind := c.State.Mind.ID
	dctx := deriveContext(ctx, c.State)
	all := append(append([]events.Event{}, c.State.NewEvents...), emitted...)
	for i, ev := range all {
		if ev.Source == volitionSource && ev.Category == events.CatVolition {
			continue // volition's own records never derive further wants
		}
		if i < len(c.State.NewEvents) && strings.HasPrefix(ev.Source, "brain") {
			continue // produced by an earlier cycle, whose hook already saw it
		}
		for _, cand := range volition.Derive(ev, dctx) {
			if _, _, err := vs.UpsertWant(ctx, r, mind, cand); err != nil {
				return err
			}
		}
	}
	for _, ev := range emitted {
		if ev.Type != events.ResponseGenerated {
			continue
		}
		msg, _ := ev.Metadata["message_id"].(string)
		if msg == "" {
			continue
		}
		if ws, err := vs.Wants(ctx, c.Tx, mind, []volition.WantStatus{volition.WantActive}, 500); err == nil {
			for _, w := range ws {
				if w.Subject == "answer:message:"+msg {
					if err := vs.ResolveWant(ctx, r, mind, w.ID, volition.WantSatisfied, "replied (event "+string(ev.ID)+")"); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, cm := range dctx.Commitments {
		switch volition.Assess(cm, c.State.Now) {
		case volition.StateDue:
			if err := vs.MarkDue(ctx, r, cm); err != nil {
				return err
			}
			due := events.Event{Type: events.CommitmentDue, Metadata: map[string]any{"commitment_id": string(cm.ID)}}
			for _, cand := range volition.Derive(due, dctx) {
				if _, _, err := vs.UpsertWant(ctx, r, mind, cand); err != nil {
					return err
				}
			}
		case volition.StateOverdue:
			if _, err := vs.CloseCommitment(ctx, r, mind, cm.ID, "broken", "the deadline "+cm.DueAt.Format(time.RFC3339)+" passed without fulfilment", false); err != nil && !errors.Is(err, volition.ErrInvalid) {
				return err
			}
		case volition.StateExpired:
			if _, err := vs.CloseCommitment(ctx, r, mind, cm.ID, "expired", "its expiry passed", false); err != nil && !errors.Is(err, volition.ErrInvalid) {
				return err
			}
		}
	}
	var live []volition.Commitment
	for _, cm := range dctx.Commitments {
		live = append(live, cm)
	}
	for _, cf := range volition.CommitmentConflicts(live, c.State.Now) {
		key := pairKey(cf.A.ID, cf.B.ID)
		if recentlyRaised(ctx, c, events.CommitmentConflict, "pair", key) {
			continue
		}
		if _, err := c.EmitNow(ctx, events.New{Type: events.CommitmentConflict, Source: volitionSource, Summary: fmt.Sprintf("commitments conflict: %q and %q — %s", cf.A.Description, cf.B.Description, cf.Reason), Salience: 0.7, Confidence: 1,
			Evidence: []events.EvidenceRef{{Kind: "commitment", ID: string(cf.A.ID)}, {Kind: "commitment", ID: string(cf.B.ID)}}, Metadata: map[string]any{"pair": key, "a": string(cf.A.ID), "b": string(cf.B.ID)},
			Payload: map[string]any{"reason": cf.Reason, "a_weight": cf.A.Weight(), "b_weight": cf.B.Weight()}}); err != nil {
			return err
		}
	}
	if _, err := vs.ExpireWants(ctx, r, mind, 40); err != nil {
		return err
	}
	return vs.SettleConflicts(ctx, c.Tx, mind, c.State.Now)
}

func pairKey(a, b ids.ID) string {
	if a > b {
		a, b = b, a
	}
	return string(a) + "|" + string(b)
}

func recentlyRaised(ctx context.Context, c *brain.Cycle, typ events.Type, key, value string) bool {
	es, ok := c.State.Services.Events.(*evstore.Store)
	if !ok {
		return false
	}
	evs, err := es.Query(ctx, evstore.Filter{MindID: c.State.Mind.ID, Types: []events.Type{typ}, Since: c.State.Now.Add(-24 * time.Hour), Limit: 200})
	if err != nil {
		return false
	}
	for _, ev := range evs {
		if ev.Metadata[key] == value {
			return true
		}
	}
	return false
}

// ---- intention formation ----

// Intend proposes deciding about the strongest active wants that have not
// been decided since they were last reinforced. Whether the decision is
// made at all depends on attention: the candidate competes for the
// workspace like any other thought.
type Intend struct{}

// Name implements brain.Process.
func (Intend) Name() string { return "intend" }

// Observe implements brain.Process.
func (Intend) Observe(ctx context.Context, s *brain.State) ([]brain.Candidate, error) {
	vs := s.Services.Volition
	if vs == nil {
		return nil, nil
	}
	ws, err := vs.Wants(ctx, nil, s.Mind.ID, []volition.WantStatus{volition.WantActive}, 200)
	if err != nil {
		return nil, err
	}
	decided := map[ids.ID]time.Time{}
	if ds, err := vs.Decisions(ctx, s.Mind.ID, volition.DecisionFilter{Limit: 500}); err == nil {
		for _, d := range ds {
			if (d.Kind == "formation" || d.Kind == "non_formation") && !d.WantID.IsZero() && d.CreatedAt.After(decided[d.WantID]) {
				decided[d.WantID] = d.CreatedAt
			}
		}
	}
	volition.SortWants(ws, s.Now)
	var out []brain.Candidate
	for _, w := range ws {
		if len(out) >= 3 {
			break
		}
		if w.Valence == "avoid" || (w.Origin == volition.OriginRequest && w.Proposal == nil) {
			continue // values constrain other wants; replies are handled by perception
		}
		if at, ok := decided[w.ID]; ok && !w.LastReinforcedAt.After(at) {
			continue // already decided; only new reinforcement reopens the question
		}
		eff := w.Effective(s.Now)
		prio := 0.4
		if w.Origin == volition.OriginGoal || w.Origin == volition.OriginOperator || w.Origin == volition.OriginCommitment {
			prio = 0.9
		} else if w.Origin == volition.OriginRequest {
			prio = 0.7
		}
		var goalRel float64
		for _, g := range s.Goals {
			if g.ID == w.GoalID {
				goalRel = g.Priority
			}
		}
		out = append(out, brain.Candidate{Kind: "intend", Subject: "intend:" + string(w.ID), Summary: fmt.Sprintf("decide whether to pursue: %s (%.2f)", w.Description, eff),
			Content:    map[string]any{"want_id": string(w.ID)},
			Features:   attention.Features{Salience: eff, Urgency: w.Urgency, GoalRelevance: goalRel, Consequence: 0.5, Confidence: 0.8, OperatorPriority: prio, Novelty: 0.3},
			Confidence: 0.8, Evidence: []events.EvidenceRef{{Kind: "want", ID: string(w.ID)}}, SourceEvents: nonZero(w.OriginEvent)})
	}
	return out, nil
}

func nonZero(id ids.ID) []ids.ID {
	if id.IsZero() {
		return nil
	}
	return []ids.ID{id}
}

// intend runs deterministic formation for an admitted want and records the
// decision, whatever it is. A formed intention is planned in the same
// transaction; nothing acts until the executor picks the plan up after commit.
func (h Handlers) intend(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	s := c.State
	vs := s.Services.Volition
	if vs == nil {
		return nil
	}
	wid, _ := t.Content["want_id"].(string)
	w, err := vs.GetWant(ctx, c.Tx, s.Mind.ID, ids.ID(wid))
	if err != nil || w.Status != volition.WantActive {
		return nil
	}
	r := recorder(c)
	r.Cause = t.EventID
	in := volition.FormationInput{Want: w, Now: s.Now, Values: s.Mind.Personality.Values, Confidence: 0.6, IgnoreCommitments: s.Mind.Config.Ablated("commitments"),
		Operator: w.Origin == volition.OriginOperator}
	if in.Commitments, err = vs.Commitments(ctx, c.Tx, s.Mind.ID, []string{"active"}, 500); err != nil {
		return err
	}
	if in.Active, err = vs.Open(ctx, c.Tx, s.Mind.ID); err != nil {
		return err
	}
	if !w.GoalID.IsZero() && s.Services.Goals != nil {
		if g, err := s.Services.Goals.Get(ctx, s.Mind.ID, w.GoalID); err == nil {
			in.Goal = &volition.GoalInfo{ID: g.ID, Title: g.Title, Priority: g.Priority, Owner: g.Owner}
			in.Operator = in.Operator || g.Owner == "operator"
		}
	}
	if !w.CommitmentID.IsZero() {
		if cm, err := vs.GetCommitment(ctx, c.Tx, s.Mind.ID, w.CommitmentID); err == nil {
			in.CommitmentWeight = cm.Weight()
		}
	}
	in.Action = ActionInfo(ctx, s.Services.Capabilities, s.Mind.ID, w.Proposal)
	if in.Action != nil {
		if err := correctSelfModel(ctx, c, *in.Action, t.EventID); err != nil {
			return err
		}
	}
	if s.Services.Capabilities != nil {
		if inv, err := s.Services.Capabilities.Inventory(ctx, s.Mind.ID); err == nil {
			for _, i := range inv {
				if i.Capability.Access == capabilities.AccessRead && !i.Capability.Locked && (i.Effect == capabilities.Allow || i.Effect == capabilities.Ask) && i.Capability.Name != "noema.note" {
					in.ReadCapabilities++
				}
			}
		}
	}
	// Value-derived tensions are made explicit before deciding.
	if in.Action != nil {
		for _, cand := range volition.Companions(w, *in.Action, in.Values) {
			avoid, _, err := vs.UpsertWant(ctx, r, s.Mind.ID, cand)
			if err != nil {
				return err
			}
			res := volition.ResolveConflict(w, avoid, s.Now, nil, map[ids.ID]float64{w.ID: in.CommitmentWeight})
			if _, _, err := vs.RecordConflict(ctx, r, w, avoid, "value", res); err != nil {
				return err
			}
			in.Constrained = in.Constrained || (res.Preferred == w.ID && res.Constrain)
			in.Deferred = in.Deferred || res.Preferred != w.ID
		}
	}
	f := volition.Form(in)
	inputs := map[string]any{"want": w.Description, "effective_strength": w.Effective(s.Now), "action": in.Action, "commitments_considered": len(in.Commitments), "open_intentions": len(in.Active), "read_capabilities": in.ReadCapabilities, "applied": f.Applied}
	if f.Outcome != volition.Formed {
		if f.Reason == volition.ReasonDuplicate {
			return nil
		}
		evID, err := c.EmitNow(ctx, events.New{Type: events.Metacognition, Source: volitionSource, Summary: "did not form an intention for \"" + w.Description + "\": " + f.Rationale, Salience: 0.3 + 0.3*w.Effective(s.Now), Confidence: 1,
			CausationID: t.EventID, Evidence: []events.EvidenceRef{{Kind: "want", ID: string(w.ID)}}, Metadata: map[string]any{"check": "intention_not_formed", "ref": string(w.ID), "reason": f.Reason, "want_id": string(w.ID)}, Payload: f})
		if err != nil {
			return err
		}
		if _, err := vs.RecordDecision(ctx, c.Tx, volition.Decision{MindID: s.Mind.ID, Kind: "non_formation", Outcome: f.Outcome, Reason: f.Reason, Rationale: f.Rationale, WantID: w.ID, Terms: f.Terms, Inputs: inputs, EventID: evID.ID, CreatedAt: s.Now}); err != nil {
			return err
		}
		if f.Suppress {
			if err := vs.SuppressWant(ctx, r, w, f.Rationale, f.Applied); err != nil {
				return err
			}
		}
		c.Runtime.Hold(workspace.Item{Key: "want:" + string(w.ID), Content: "not pursuing: " + w.Description + " (" + f.Reason + ")", Kind: "note", Ref: string(w.ID), SourceEvent: evID.ID})
		return nil
	}
	if f.Displace != "" {
		if _, err := vs.Apply(ctx, r, s.Mind.ID, f.Displace, volition.Reconsideration{Outcome: volition.Suspend, Reason: "capacity: a higher-priority intention formed (" + w.Description + ")"}, volition.Trigger{Kind: volition.TriggerSuperior, Detail: w.Description}); err != nil && !errors.Is(err, volition.ErrClosed) {
			return err
		}
		if displaced, err := vs.GetIntention(ctx, c.Tx, s.Mind.ID, f.Displace); err == nil && len(displaced.SourceWants) > 0 {
			if other, err := vs.GetWant(ctx, c.Tx, s.Mind.ID, displaced.SourceWants[0]); err == nil {
				res := volition.ResolveConflict(w, other, s.Now, nil, map[ids.ID]float64{w.ID: in.CommitmentWeight})
				vs.RecordConflict(ctx, r, w, other, "priority", res) //nolint:errcheck
			}
		}
	}
	var required []string
	if w.Proposal != nil && w.Proposal.Capability != "" {
		required = []string{w.Proposal.Capability}
	}
	expected := w.Description
	if in.Action != nil {
		expected = "the effect of " + in.Action.Capability + " on " + in.Action.Target + " is observed"
	}
	terms := map[string]any{"score": f.Score, "terms": f.Terms, "rationale": f.Rationale, "applied_commitments": f.Applied, "constrained": in.Constrained}
	it, err := vs.CreateIntention(ctx, r, volition.Intention{MindID: s.Mind.ID, Description: f.Description, DesiredOutcome: f.DesiredOutcome, Kind: f.Kind, SourceWants: []ids.ID{w.ID}, GoalID: w.GoalID, CommitmentID: w.CommitmentID,
		ExpectedResult: expected, Confidence: in.Confidence, Priority: f.Priority, RequireApproval: f.RequireApproval, Proposal: w.Proposal, RequiredCapabilities: required, Formation: terms})
	if err != nil {
		return err
	}
	if _, err := vs.RecordDecision(ctx, c.Tx, volition.Decision{MindID: s.Mind.ID, Kind: "formation", Outcome: string(f.Kind), Reason: f.Reason, Rationale: f.Rationale, WantID: w.ID, IntentionID: it.ID, Terms: f.Terms, Inputs: inputs, EventID: it.FormedEvent, CreatedAt: s.Now}); err != nil {
		return err
	}
	c.Runtime.Hold(workspace.Item{Key: "intention:" + string(it.ID), Content: "intends to " + it.Description, Kind: "thought", Ref: string(it.ID), SourceEvent: it.FormedEvent})
	if s.Services.Executor == nil || it.Kind == volition.IntentRespond {
		return nil
	}
	if s.Mode == brain.ModeDreaming {
		return nil // dreams may intend; plans are made awake
	}
	if _, err := s.Services.Executor.Start(ctx, r, it); err != nil {
		reason := "no possible plan: " + err.Error()
		outcome := volition.Abandon
		if errors.Is(err, planning.ErrInvalidPlan) {
			outcome, reason = volition.Block, "the proposed plan failed validation: "+err.Error()
		}
		if _, aerr := vs.Apply(ctx, r, s.Mind.ID, it.ID, volition.Reconsideration{Outcome: outcome, BlockedKind: volition.BlockPrereq, Reason: reason}, volition.Trigger{Kind: "planning", Detail: err.Error()}); aerr != nil {
			return aerr
		}
		if outcome == volition.Abandon {
			return vs.SuppressWant(ctx, r, w, reason, nil)
		}
	}
	return nil
}

// want records an explicit action request as a want with its proposed means.
func (h Handlers) want(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	vs := c.State.Services.Volition
	if vs == nil {
		return nil
	}
	src, _ := t.Content["source_event"].(string)
	actor, _ := t.Content["actor"].(string)
	text, _ := t.Content["text"].(string)
	operator, _ := t.Content["operator"].(bool)
	p, ok := t.Content["proposal"].(volition.Proposal)
	if !ok {
		return nil
	}
	ev := events.Event{ID: ids.ID(src)}
	r := recorder(c)
	r.Cause = t.EventID
	for _, cand := range volition.FromRequest(ev, actor, text, p, deriveContext(ctx, c.State), operator) {
		w, _, err := vs.UpsertWant(ctx, r, c.State.Mind.ID, cand)
		if err != nil {
			return err
		}
		c.Runtime.Hold(workspace.Item{Key: "want:" + string(w.ID), Content: "wants: " + w.Description, Kind: "thought", Ref: string(w.ID), SourceEvent: ids.ID(src)})
	}
	return nil
}

// correctSelfModel compares what the self-model says about a capability
// with what policy actually gives now. A mismatch is recorded as a
// metacognitive finding and the self-model entry is corrected: reality, as
// the capability layer reports it, wins over the mind's belief about itself.
func correctSelfModel(ctx context.Context, c *brain.Cycle, a volition.ActionInfo, cause ids.ID) error {
	s := c.State
	if s.Services.Minds == nil {
		return nil
	}
	actual := a.Effect
	if actual == "locked" || actual == "unknown" {
		actual = "unavailable"
	}
	for i, entry := range s.SelfModel.Capabilities {
		if entry.Name != a.Capability || entry.Policy == actual {
			continue
		}
		ev, err := c.EmitNow(ctx, events.New{Type: events.Metacognition, Source: "brain.selfmodel", Summary: fmt.Sprintf("self-model said %s is %s, but policy gives %s; corrected", a.Capability, entry.Policy, actual),
			Salience: 0.6, Confidence: 1, CausationID: cause, Metadata: map[string]any{"check": "self_model_mismatch", "ref": a.Capability, "stated": entry.Policy, "actual": actual}})
		if err != nil {
			return err
		}
		sm, err := s.Services.Minds.GetSelfModel(ctx, s.Mind.ID)
		if err != nil {
			return nil
		}
		for j := range sm.Capabilities {
			if sm.Capabilities[j].Name == a.Capability {
				sm.Capabilities[j].Policy = actual
			}
		}
		if err := s.Services.Minds.SaveSelfModel(ctx, s.Mind.ID, sm); err != nil {
			return err
		}
		s.SelfModel.Capabilities[i].Policy = actual
		c.Emit(events.New{Type: events.SelfModelUpdated, Source: "brain.selfmodel", Summary: "self-model corrected: " + a.Capability + " is " + actual, Salience: 0.4, Confidence: 1, CausationID: ev.ID,
			Metadata: map[string]any{"correction": true, "capability": a.Capability, "from": entry.Policy, "to": actual}})
	}
	return nil
}
