package volition

import (
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

var now = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

func neutral() DeriveContext {
	return DeriveContext{Now: now, Drives: map[string]float64{}, Values: map[string]float64{}, Goals: map[ids.ID]GoalInfo{}, Commitments: map[ids.ID]Commitment{}}
}

func TestDeriveOnlyFromStatesThatCallForChange(t *testing.T) {
	d := neutral()
	for _, typ := range []events.Type{events.Observation, events.MemoryCreated, events.BeliefUpdated, events.CycleCompleted, events.ToolExecuted} {
		if c := Derive(events.Event{Type: typ, Summary: "x", Salience: 1}, d); len(c) != 0 {
			t.Fatalf("%s produced a want: %+v", typ, c)
		}
	}
	pe := events.Event{ID: ids.New(), Type: events.PredictionError, Salience: 0.9, Summary: "prediction refuted: atlas recovers", Metadata: map[string]any{"prediction_id": "p1"}}
	c := Derive(pe, d)
	if len(c) != 1 || c[0].Subject != "understand:prediction:p1" || c[0].Proposal.Kind != "investigate" || c[0].Terms["base"] == 0 {
		t.Fatalf("prediction error: %+v", c)
	}
	// Weak signal below the threshold stores nothing.
	low := d
	low.Drives = map[string]float64{"uncertainty_reduction": 0, "curiosity": 0}
	low.Values = map[string]float64{"accuracy": 0, "evidence": 0}
	if c := Derive(events.Event{Type: events.PredictionError, Salience: 0, Metadata: map[string]any{"prediction_id": "p2"}}, low); len(c) != 0 {
		t.Fatalf("below-threshold want stored: %+v", c)
	}
	// Drives and values modulate strength visibly.
	hi := d
	hi.Drives = map[string]float64{"uncertainty_reduction": 1, "curiosity": 1}
	if Derive(pe, hi)[0].Strength <= c[0].Strength {
		t.Fatal("drives did not raise strength")
	}
	gid := ids.New()
	d.Goals[gid] = GoalInfo{ID: gid, Title: "Diagnose why atlas is unhealthy", Subject: "atlas", Owner: "operator", Priority: 0.8}
	c = Derive(events.Event{Type: events.GoalCreated, Metadata: map[string]any{"goal_id": string(gid)}}, d)
	if len(c) != 1 || c[0].GoalID != gid || c[0].Proposal == nil || c[0].Proposal.Kind != "investigate" || c[0].Proposal.Subject != "atlas" {
		t.Fatalf("operator goal: %+v", c)
	}
	cur := ids.New()
	d.Goals[cur] = GoalInfo{ID: cur, Title: "why?", Owner: "mind"}
	if len(Derive(events.Event{Type: events.GoalCreated, Metadata: map[string]any{"goal_id": string(cur)}}, d)) != 0 {
		t.Fatal("curiosity goal produced a second want")
	}
	q := events.Event{Type: events.Perception, Summary: "perceived question from andy: why is atlas down?", Metadata: map[string]any{"intent": "question", "conversation_id": "c", "message_id": "m"}}
	if c := Derive(q, d); len(c) != 1 || !strings.Contains(c[0].Description, "andy") || c[0].Urgency < 0.6 {
		t.Fatalf("question: %+v", c)
	}
	st := q
	st.Metadata = map[string]any{"intent": "statement", "conversation_id": "c", "message_id": "m"}
	if len(Derive(st, d)) != 0 {
		t.Fatal("a statement produced a want")
	}
}

func TestProposalFromText(t *testing.T) {
	cases := map[string]string{
		"please fetch https://status.example.org/atlas.": "http.fetch https://status.example.org/atlas",
		"Restart atlas-worker":                           "service.restart atlas-worker",
		"read the logs for atlas-worker":                 "logs.query atlas-worker",
		"what is the status of nginx?":                   "service.status nginx",
	}
	for text, want := range cases {
		p := ProposalFromText(text, "")
		if p == nil || p.Capability+" "+p.Target != want {
			t.Errorf("%q → %+v", text, p)
		}
	}
	if p := ProposalFromText("Determine why Atlas is unhealthy", ""); p == nil || p.Kind != "investigate" || p.Subject != "Atlas is unhealthy" {
		t.Fatalf("investigate: %+v", p)
	}
	if ProposalFromText("tell me a joke", "") != nil {
		t.Fatal("invented a means")
	}
}

func want(desc string, s, u float64, valence string) Want {
	return Want{ID: ids.New(), Description: desc, Strength: s, Urgency: u, Valence: valence, LastReinforcedAt: now, Status: WantActive}
}

func TestWantDecay(t *testing.T) {
	w := want("x", 0.8, 0, "approach")
	if w.Effective(now.Add(WantHalfLife)) != 0.4 {
		t.Fatalf("half-life: %v", w.Effective(now.Add(WantHalfLife)))
	}
}

func TestConflictResolutionIsExplicitAndHasNoFixedHierarchy(t *testing.T) {
	investigate := want("continue the interesting investigation", 0.9, 0.2, "approach")
	report := want("fulfil the commitment to deliver the report", 0.5, 0.9, "approach")
	// Without the commitment's weight the investigation wins.
	r := ResolveConflict(investigate, report, now, nil, nil)
	if r.Preferred != investigate.ID {
		t.Fatalf("stronger want should win without normative weight: %+v", r)
	}
	// With an overdue commitment's weight the report wins, and both scores are recorded.
	r = ResolveConflict(investigate, report, now, nil, map[ids.ID]float64{report.ID: 0.9})
	if r.Preferred != report.ID || r.Rationale["a"] == nil || r.Rationale["b"] == nil || r.Rationale["rule"] == nil {
		t.Fatalf("commitment weight ignored or rationale missing: %+v", r)
	}
	// A weak commitment does not automatically beat a much stronger want.
	weak := want("fulfil a minor promise", 0.2, 0.1, "approach")
	if ResolveConflict(investigate, weak, now, nil, map[ids.ID]float64{weak.ID: 0.2}).Preferred != investigate.ID {
		t.Fatal("commitment always beat want")
	}
	// Approach against a value-derived avoid want.
	restart := want("restore service quickly", 0.8, 0.8, "approach")
	avoid := want("avoid unnecessary disruption", 0.5, 0.2, "avoid")
	r = ResolveConflict(restart, avoid, now, nil, nil)
	if r.Preferred != restart.ID || r.Outcome != "preferred_with_constraint" || !r.Constrain {
		t.Fatalf("approach vs value: %+v", r)
	}
	avoid.Strength = 1
	restart.Strength, restart.Urgency = 0.4, 0.2
	if r = ResolveConflict(restart, avoid, now, nil, nil); r.Outcome != "deferred" || r.Preferred != avoid.ID {
		t.Fatalf("value should defer weak approach: %+v", r)
	}
}

var noProd = Commitment{ID: ids.New(), Description: "do not modify production without approval", Kind: KindOperatorInstruction, Polarity: "refrain", Unless: "approval",
	Matcher: Matcher{Access: "write", Targets: []string{"atlas-*", "prod-*"}}, Strength: 1, Priority: 1, Status: "active"}

func restartAction(effect string) *ActionInfo {
	return &ActionInfo{Capability: "service.restart", Target: "atlas-worker.service", Access: "write", Risk: "high", Effect: effect, SideEffects: "stops and starts the unit", WallSeconds: 10}
}

func TestFormationUnderCommitments(t *testing.T) {
	w := want("restart atlas", 0.9, 0.9, "approach")
	w.Proposal = &Proposal{Kind: "capability", Capability: "service.restart", Target: "atlas-worker.service"}
	// Policy would allow; the commitment turns the intention into a request for approval.
	f := Form(FormationInput{Want: w, Now: now, Commitments: []Commitment{noProd}, Action: restartAction("allow"), Operator: true})
	if f.Outcome != Formed || f.Kind != IntentRequestApproval || !f.RequireApproval || len(f.Applied) != 1 || !strings.Contains(f.Rationale, "requires approval") {
		t.Fatalf("approval commitment: %+v", f)
	}
	// Refrain without exception: no intention, want suppressed but represented.
	never := noProd
	never.ID, never.Unless, never.Description = ids.New(), "", "never restart atlas"
	f = Form(FormationInput{Want: w, Now: now, Commitments: []Commitment{never}, Action: restartAction("allow"), Operator: true})
	if f.Outcome != NotFormed || f.Reason != ReasonForbidden || !f.Suppress {
		t.Fatalf("forbidding commitment: %+v", f)
	}
	// Ablation: commitments ignored.
	if f = Form(FormationInput{Want: w, Now: now, Commitments: []Commitment{never}, Action: restartAction("allow"), IgnoreCommitments: true, Operator: true}); f.Outcome != Formed || f.RequireApproval {
		t.Fatalf("ablation: %+v", f)
	}
	// Expired commitments do not bind; commitments on other targets do not bind.
	exp := never
	past := now.Add(-time.Minute)
	exp.ExpiresAt = &past
	if f = Form(FormationInput{Want: w, Now: now, Commitments: []Commitment{exp}, Action: restartAction("allow"), Operator: true}); f.Outcome != Formed {
		t.Fatalf("expired commitment bound: %+v", f)
	}
	other := never
	other.Matcher.Targets = []string{"db-*"}
	if f = Form(FormationInput{Want: w, Now: now, Commitments: []Commitment{other}, Action: restartAction("allow"), Operator: true}); f.Outcome != Formed {
		t.Fatalf("unrelated commitment bound: %+v", f)
	}
	// Denied and unknown capabilities: no intention, and nothing is requested.
	for effect, reason := range map[string]string{"deny": ReasonDenied, "locked": ReasonUnavailable, "unknown": ReasonUnavailable} {
		if f = Form(FormationInput{Want: w, Now: now, Action: restartAction(effect), Operator: true}); f.Outcome != NotFormed || f.Reason != reason {
			t.Fatalf("%s: %+v", effect, f)
		}
	}
	// Constrained by a value conflict: request approval even when policy allows.
	if f = Form(FormationInput{Want: w, Now: now, Action: restartAction("allow"), Constrained: true, Operator: true}); !f.RequireApproval || f.Kind != IntentRequestApproval {
		t.Fatalf("constraint: %+v", f)
	}
	// Risk and caution lower the score.
	cautious := Form(FormationInput{Want: w, Now: now, Action: restartAction("ask"), Values: map[string]float64{"caution": 1}, Operator: true})
	bold := Form(FormationInput{Want: w, Now: now, Action: restartAction("ask"), Values: map[string]float64{"caution": 0}, Operator: true})
	if cautious.Score >= bold.Score {
		t.Fatalf("caution ignored: %v vs %v", cautious.Score, bold.Score)
	}
}

func TestFormationEdges(t *testing.T) {
	inv := want("understand the failure", 0.8, 0.4, "approach")
	inv.Proposal = &Proposal{Kind: "investigate", Subject: "atlas"}
	if f := Form(FormationInput{Want: inv, Now: now, ReadCapabilities: 0}); f.Outcome != NotFormed || f.Reason != ReasonNoPlan {
		t.Fatalf("want with no possible plan: %+v", f)
	}
	if f := Form(FormationInput{Want: inv, Now: now, ReadCapabilities: 3}); f.Outcome != Formed || f.Kind != IntentInvestigate {
		t.Fatalf("investigate: %+v", f)
	}
	faint := inv
	faint.Strength, faint.Urgency = 0.1, 0
	if f := Form(FormationInput{Want: faint, Now: now, ReadCapabilities: 3}); f.Outcome != NotFormed || f.Reason != ReasonBelowThreshold {
		t.Fatalf("threshold: %+v", f)
	}
	dup := Intention{ID: ids.New(), SourceWants: []ids.ID{inv.ID}, Status: IntentionActive}
	if f := Form(FormationInput{Want: inv, Now: now, ReadCapabilities: 3, Active: []Intention{dup}}); f.Reason != ReasonDuplicate {
		t.Fatalf("duplicate: %+v", f)
	}
	full := []Intention{{ID: ids.New(), Status: IntentionActive, Priority: 0.9}, {ID: ids.New(), Status: IntentionActive, Priority: 0.2}}
	if f := Form(FormationInput{Want: inv, Now: now, ReadCapabilities: 3, Active: full, MaxActive: 2}); f.Outcome != Formed || f.Displace != full[1].ID {
		t.Fatalf("displacement: %+v", f)
	}
	full[1].Priority = 0.95
	if f := Form(FormationInput{Want: inv, Now: now, ReadCapabilities: 3, Active: full, MaxActive: 2}); f.Reason != ReasonCapacity {
		t.Fatalf("capacity: %+v", f)
	}
	if f := Form(FormationInput{Want: inv, Now: now, ReadCapabilities: 3, Deferred: true}); f.Reason != ReasonDeferred || !f.Suppress {
		t.Fatalf("deferred: %+v", f)
	}
}

func TestCommitmentChecks(t *testing.T) {
	due := now.Add(90 * time.Minute)
	report := Commitment{ID: ids.New(), Subject: "report", Description: "deliver the report to Andy today", Kind: KindPromise, Polarity: "do", Strength: 0.9, Priority: 0.8, Status: "active", DueAt: &due, CreatedAt: now.Add(-time.Hour),
		Matcher: Matcher{Capabilities: []string{"send_email"}}}
	if Assess(report, now) != StateDue {
		t.Fatal("not due inside the window")
	}
	if Assess(report, now.Add(2*time.Hour)) != StateOverdue {
		t.Fatal("not overdue after grace")
	}
	noted := report
	noted.DueNotifiedAt = &now
	if Assess(noted, now) != StateFine {
		t.Fatal("due raised twice")
	}
	if why := Impossible(report, map[string]string{"send_email": "deny"}, now); why == "" {
		t.Fatal("impossible commitment not detected")
	}
	if why := Impossible(report, map[string]string{"send_email": "ask"}, now); why != "" {
		t.Fatalf("possible commitment flagged: %s", why)
	}
	noEmail := Commitment{ID: ids.New(), Subject: "email", Description: "never send email", Kind: KindOperatorInstruction, Polarity: "refrain", Matcher: Matcher{Capabilities: []string{"send_*"}}, Status: "active", Strength: 1, Priority: 1}
	cs := CommitmentConflicts([]Commitment{report, noEmail, noProd}, now)
	if len(cs) != 1 || cs[0].A.ID != report.ID {
		t.Fatalf("conflicts: %+v", cs)
	}
	if noProd.Weight() <= (Commitment{Kind: KindPriorDecision, Strength: 1, Priority: 1}).Weight() {
		t.Fatal("operator instruction should weigh more than a prior decision")
	}
	if (Commitment{Polarity: "refrain", Status: "active"}).Matches(*restartAction("allow")) {
		t.Fatal("empty matcher matched")
	}
}

func TestCompanions(t *testing.T) {
	w := want("restart", 0.9, 0.9, "approach")
	c := Companions(w, *restartAction("ask"), map[string]float64{"caution": 0.8})
	if len(c) != 1 || c[0].Valence != "avoid" || !strings.HasPrefix(c[0].Subject, "avoid:disruption:") {
		t.Fatalf("caution companion: %+v", c)
	}
	logs := ActionInfo{Capability: "logs.query", Target: "atlas", Access: "read", Risk: "medium", SideEffects: "none; the output may contain personal data"}
	if c := Companions(w, logs, map[string]float64{"privacy": 0.9, "caution": 1}); len(c) != 1 || !strings.HasPrefix(c[0].Subject, "avoid:exposure:") {
		t.Fatalf("privacy companion: %+v", c)
	}
	if c := Companions(w, logs, map[string]float64{"privacy": 0.1}); len(c) != 0 {
		t.Fatal("weak value produced a companion")
	}
}

func TestReconsider(t *testing.T) {
	it := Intention{ID: ids.New(), Kind: IntentAct, Status: IntentionActive}
	check := func(tr Trigger, rc ReconsiderContext, want string) {
		t.Helper()
		rc.Now = now
		if got := Reconsider(it, tr, rc); got.Outcome != want || got.Reason == "" {
			t.Fatalf("%s: got %+v, want %s", tr.Kind, got, want)
		}
	}
	check(Trigger{Kind: TriggerApprovalDenied, Detail: "not during business hours"}, ReconsiderContext{}, Replan)
	check(Trigger{Kind: TriggerApprovalDenied}, ReconsiderContext{Replans: 3}, Abandon)
	check(Trigger{Kind: TriggerExpectationFailed, Detail: "error rate still 12%"}, ReconsiderContext{Replans: 1}, Replan)
	check(Trigger{Kind: TriggerExpectationFailed}, ReconsiderContext{Replans: 3, MaxReplans: 3}, Abandon)
	check(Trigger{Kind: TriggerCapabilityChanged, Action: restartAction("deny")}, ReconsiderContext{}, Replan)
	check(Trigger{Kind: TriggerCapabilityChanged, Action: restartAction("ask")}, ReconsiderContext{}, Block)
	check(Trigger{Kind: TriggerResourceExhausted, Detail: "capability invocations"}, ReconsiderContext{}, Suspend)
	check(Trigger{Kind: TriggerPlanCompleted}, ReconsiderContext{}, Fulfil)
	check(Trigger{Kind: TriggerGoalCompleted}, ReconsiderContext{}, Abandon)
	check(Trigger{Kind: TriggerWorldChanged, OutcomeObserved: true, Detail: "atlas already healthy"}, ReconsiderContext{}, Fulfil)
	check(Trigger{Kind: TriggerCommitmentChanged, Commitment: &noProd, Action: restartAction("allow")}, ReconsiderContext{}, Tighten)
	never := noProd
	never.Unless = ""
	check(Trigger{Kind: TriggerCommitmentChanged, Commitment: &never, Action: restartAction("allow")}, ReconsiderContext{}, Abandon)
	unrelated := never
	unrelated.Matcher.Targets = []string{"db-*"}
	check(Trigger{Kind: TriggerCommitmentChanged, Commitment: &unrelated, Action: restartAction("allow")}, ReconsiderContext{}, Keep)
	it.Status = IntentionBlocked
	check(Trigger{Kind: TriggerRequestGranted}, ReconsiderContext{}, Resume)
}
