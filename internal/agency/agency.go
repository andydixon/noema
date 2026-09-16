// Package agency answers "why did this happen?" from recorded state and
// classifies whether the mind caused an outcome (docs/agency.md). It never
// generates reasons: every element of an explanation is a stored row or
// event, and anything not recorded is listed as a gap rather than filled in.
// Hidden model reasoning is not exposed because it is not stored; what is
// shown is cognitive state and provenance.
package agency

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Service builds explanations and attributions.
type Service struct {
	DB          *pgxpool.Pool
	Caps        *capabilities.Service
	Plans       *planning.Store
	Volition    *volition.Store
	Goals       *goals.Store
	Beliefs     *beliefs.Store
	Predictions *predictions.Store
	Events      *evstore.Store
}

// Ref names the thing to explain.
type Ref struct {
	Kind string `json:"kind"` // execution | approval | capability_request | event | intention | plan_step
	ID   ids.ID `json:"id"`
}

// ActionView is what the capability layer recorded.
type ActionView struct {
	Capability  string     `json:"capability"`
	Target      string     `json:"target"`
	Status      string     `json:"status"` // executed | failed | pending | approved | denied | expired | withdrawn | granted
	ExecutionID ids.ID     `json:"execution_id,omitempty"`
	ApprovalID  ids.ID     `json:"approval_id,omitempty"`
	RequestID   ids.ID     `json:"request_id,omitempty"`
	DecidedBy   string     `json:"decided_by,omitempty"`
	Note        string     `json:"note,omitempty"`
	Result      string     `json:"result,omitempty"`
	Origin      string     `json:"origin,omitempty"`
	At          *time.Time `json:"at,omitempty"`
}

// Element is one recorded item in an explanation.
type Element struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Text   string `json:"text"`
	Detail string `json:"detail,omitempty"`
}

// Explanation is the forensic answer to "why did this happen?".
type Explanation struct {
	Subject          Ref                   `json:"subject"`
	Action           *ActionView           `json:"action,omitempty"`
	Step             *planning.Step        `json:"step,omitempty"`
	Plan             *Element              `json:"plan,omitempty"`
	Intention        *volition.Intention   `json:"intention,omitempty"`
	Subordinate      *volition.Intention   `json:"subordinate_intention,omitempty"` // e.g. "obtain approval to …" formed while the main intention waited
	Wants            []volition.Want       `json:"wants"`
	Goal             *Element              `json:"goal,omitempty"`
	Beliefs          []Element             `json:"beliefs"`
	Evidence         []Element             `json:"evidence"`
	ConflictingWants []Element             `json:"conflicting_wants"`
	Commitments      []volition.Commitment `json:"commitments"`
	Decisions        []volition.Decision   `json:"decisions"`
	Resolution       []string              `json:"resolution"`
	Expectation      *Element              `json:"expectation,omitempty"`
	Attribution      *Attribution          `json:"attribution,omitempty"`
	Confidence       float64               `json:"confidence"`
	Gaps             []string              `json:"gaps"`
}

// Facts converts an explanation into the recorded facts an introspective
// answer is scored against.
func (x Explanation) Facts() research.Facts {
	var f research.Facts
	add := func(kind, text string) {
		if strings.TrimSpace(text) != "" {
			f.Items = append(f.Items, research.Fact{Kind: kind, Text: text})
		}
	}
	if x.Action != nil {
		f.Actions = append(f.Actions, research.ActionFact{Capability: x.Action.Capability, Target: x.Action.Target, Status: x.Action.Status})
		add("action", x.Action.Capability+" on "+x.Action.Target+" "+x.Action.Status)
	}
	if x.Intention != nil {
		add("intention", x.Intention.Description)
		add("intention", fmt.Sprint(x.Intention.Formation["rationale"]))
	}
	if x.Subordinate != nil {
		add("intention", x.Subordinate.Description)
	}
	for _, w := range x.Wants {
		add("want", w.Description)
	}
	if x.Goal != nil {
		add("goal", x.Goal.Text)
	}
	for _, b := range x.Beliefs {
		add("belief", b.Text)
	}
	for _, c := range x.Commitments {
		add("commitment", c.Description)
	}
	for _, w := range x.ConflictingWants {
		add("conflict", w.Text)
	}
	for _, r := range x.Resolution {
		add("resolution", r)
	}
	for _, d := range x.Decisions {
		add("decision", d.Rationale)
	}
	if x.Expectation != nil {
		add("expectation", x.Expectation.Text)
	}
	return f
}

// Points renders the explanation as plain statements, in causal order.
func (x Explanation) Points() []string {
	var p []string
	if x.Action != nil {
		p = append(p, fmt.Sprintf("Action: %s on %s — %s.", x.Action.Capability, firstNonEmpty(x.Action.Target, "no target"), x.Action.Status))
	}
	if x.Step != nil {
		p = append(p, fmt.Sprintf("Immediate cause: plan step %s (%s).", x.Step.Key, x.Step.Title))
	}
	if x.Intention != nil {
		p = append(p, fmt.Sprintf("Intention: %s (%s, priority %.2f).", x.Intention.Description, x.Intention.Status, x.Intention.Priority))
	}
	if x.Subordinate != nil {
		p = append(p, "Subordinate intention while waiting: "+x.Subordinate.Description+" ("+string(x.Subordinate.Status)+").")
	}
	for _, w := range x.Wants {
		p = append(p, fmt.Sprintf("Originating want: %s (%s, strength %.2f).", w.Description, w.Origin, w.Strength))
	}
	if x.Goal != nil {
		p = append(p, "Goal: "+x.Goal.Text+".")
	}
	for _, b := range x.Beliefs {
		p = append(p, "Supporting belief: "+b.Text+" ("+b.Detail+").")
	}
	for _, e := range x.Evidence {
		p = append(p, "Evidence: "+e.Text+".")
	}
	for _, w := range x.ConflictingWants {
		p = append(p, "Conflicting want: "+w.Text+" — "+w.Detail+".")
	}
	for _, c := range x.Commitments {
		p = append(p, fmt.Sprintf("Commitment: %s (%s).", c.Description, c.Kind))
	}
	for _, r := range x.Resolution {
		p = append(p, "Resolution: "+r)
	}
	if x.Expectation != nil {
		p = append(p, "Expected: "+x.Expectation.Text+" — "+x.Expectation.Detail+".")
	}
	if x.Attribution != nil {
		p = append(p, "Attribution: "+x.Attribution.Class+" — "+x.Attribution.Reason)
	}
	if x.Confidence > 0 {
		p = append(p, fmt.Sprintf("Confidence: %.2f (the intention's expected chance of achieving its outcome).", x.Confidence))
	}
	for _, g := range x.Gaps {
		p = append(p, "Not recorded: "+g+".")
	}
	return p
}

// Explain assembles the recorded causal chain behind an action, approval,
// request, event, plan step or intention of a mind.
func (s *Service) Explain(ctx context.Context, mind ids.ID, ref Ref) (Explanation, error) {
	x := Explanation{Subject: ref, Wants: []volition.Want{}, Beliefs: []Element{}, Evidence: []Element{}, ConflictingWants: []Element{}, Commitments: []volition.Commitment{}, Decisions: []volition.Decision{}, Resolution: []string{}, Gaps: []string{}}
	var stepID, intentionID ids.ID
	switch ref.Kind {
	case "execution":
		e, err := s.Caps.GetExecution(ctx, ref.ID)
		if err != nil || e.MindID != mind {
			return x, fmt.Errorf("execution not found for this mind")
		}
		status := "executed"
		if e.Error != "" {
			status = "failed"
		}
		at := e.CreatedAt
		x.Action = &ActionView{Capability: e.Capability, Target: e.Target, Status: status, ExecutionID: e.ID, ApprovalID: e.ApprovalID, Origin: e.Origin, At: &at, Result: summaryOf(e.Result, e.Error)}
		stepID = stepFromOrigin(e.Origin)
		if !e.ApprovalID.IsZero() {
			if a, err := s.Caps.GetApproval(ctx, e.ApprovalID); err == nil {
				x.Action.DecidedBy, x.Action.Note = a.DecisionBy, a.DecisionNote
				x.Resolution = append(x.Resolution, fmt.Sprintf("approval %s was %s by %s%s", short(a.ID), a.Status, a.DecisionBy, noteSuffix(a.DecisionNote)))
			}
		}
	case "approval":
		a, err := s.Caps.GetApproval(ctx, ref.ID)
		if err != nil || a.MindID != mind {
			return x, fmt.Errorf("approval not found for this mind")
		}
		at := a.CreatedAt
		x.Action = &ActionView{Capability: a.Capability, Target: a.Target, Status: a.Status, ApprovalID: a.ID, DecidedBy: a.DecisionBy, Note: a.DecisionNote, Origin: a.Origin, At: &at, Result: summaryOf(a.Result, a.Error)}
		stepID = stepFromOrigin(a.Origin)
		x.Resolution = append(x.Resolution, fmt.Sprintf("approval %s requested: %s", short(a.ID), a.Reason))
		if a.Status != "pending" {
			x.Resolution = append(x.Resolution, fmt.Sprintf("it was %s by %s%s", a.Status, firstNonEmpty(a.DecisionBy, "nobody"), noteSuffix(a.DecisionNote)))
		}
	case "capability_request":
		r, err := s.Caps.GetCapabilityRequest(ctx, ref.ID)
		if err != nil || r.MindID != mind {
			return x, fmt.Errorf("capability request not found for this mind")
		}
		at := r.CreatedAt
		x.Action = &ActionView{Capability: r.Capability, Target: r.TargetPattern, Status: r.Status, RequestID: r.ID, DecidedBy: r.DecidedBy, Note: r.DecisionNote, At: &at}
		stepID, intentionID = r.StepID, r.IntentionID
		x.Resolution = append(x.Resolution, "requested temporary access because: "+r.Reason)
		if r.Risk != "" {
			x.Resolution = append(x.Resolution, "stated risk: "+r.Risk+"; mitigation: "+firstNonEmpty(r.Mitigation, "none")+"; fallback: "+firstNonEmpty(r.Fallback, "none"))
		}
	case "plan_step":
		stepID = ref.ID
	case "intention":
		intentionID = ref.ID
	case "event":
		ev, err := s.Events.Get(ctx, ref.ID)
		if err != nil || ev.MindID != mind {
			return x, fmt.Errorf("event not found for this mind")
		}
		meta := func(k string) ids.ID { v, _ := ev.Metadata[k].(string); return ids.ID(v) }
		switch {
		case !meta("execution_id").IsZero():
			return s.explainWithSubject(ctx, mind, Ref{Kind: "execution", ID: meta("execution_id")}, ref)
		case !meta("approval_id").IsZero():
			return s.explainWithSubject(ctx, mind, Ref{Kind: "approval", ID: meta("approval_id")}, ref)
		case !meta("request_id").IsZero():
			return s.explainWithSubject(ctx, mind, Ref{Kind: "capability_request", ID: meta("request_id")}, ref)
		case !meta("step_id").IsZero():
			stepID = meta("step_id")
		case !meta("intention_id").IsZero():
			intentionID = meta("intention_id")
		default:
			x.Gaps = append(x.Gaps, "this event does not reference an action, plan or intention; see its causal timeline instead")
			return x, nil
		}
	default:
		return x, fmt.Errorf("cannot explain a %q", ref.Kind)
	}
	if !stepID.IsZero() && s.Plans != nil {
		if st, err := s.Plans.GetStep(ctx, stepID); err == nil && st.MindID == mind {
			x.Step = &st
			if p, err := s.Plans.Get(ctx, nil, st.PlanID); err == nil {
				x.Plan = &Element{Kind: "plan", ID: string(p.ID), Text: p.Objective, Detail: fmt.Sprintf("revision %d, %s strategy, %s", p.Revision, p.Strategy, p.Status)}
				intentionID = p.IntentionID
				for _, a := range p.Assumptions {
					if !a.BeliefID.IsZero() {
						s.addBelief(ctx, mind, &x, a.BeliefID)
					}
				}
			}
			if st.Expectation != nil {
				e := &Element{Kind: "expectation", Text: st.Expectation.Description, Detail: "not yet resolved"}
				if !st.PredictionID.IsZero() && s.Predictions != nil {
					if pr, err := s.Predictions.Get(ctx, mind, st.PredictionID); err == nil {
						e.ID, e.Detail = string(pr.ID), string(pr.Status)
					}
				}
				x.Expectation = e
			}
		}
	} else if x.Action != nil && x.Action.Origin == "" {
		x.Gaps = append(x.Gaps, "no plan step or intention asked for this action; it was invoked directly (for example by an operator through the API)")
	}
	if intentionID.IsZero() {
		if x.Step == nil && x.Action != nil && x.Action.Origin != "" {
			x.Gaps = append(x.Gaps, "the plan step "+x.Action.Origin+" is no longer available")
		}
		return x, nil
	}
	it, err := s.Volition.GetIntention(ctx, nil, mind, intentionID)
	if err != nil {
		x.Gaps = append(x.Gaps, "the intention "+string(intentionID)+" is not recorded")
		return x, nil
	}
	if !it.ParentID.IsZero() {
		if parent, err := s.Volition.GetIntention(ctx, nil, mind, it.ParentID); err == nil {
			sub := it
			x.Subordinate = &sub
			it = parent // explain the substantive intention; the subordinate is kept alongside
		}
	}
	if x.Subordinate == nil && x.Step != nil && !x.Step.IntentionID.IsZero() {
		if sub, err := s.Volition.GetIntention(ctx, nil, mind, x.Step.IntentionID); err == nil {
			x.Subordinate = &sub
		}
	}
	x.Intention = &it
	x.Confidence = it.Confidence
	for _, b := range it.BeliefIDs {
		s.addBelief(ctx, mind, &x, b)
	}
	wantIDs := append([]ids.ID{}, it.SourceWants...)
	for _, w := range wantIDs {
		if want, err := s.Volition.GetWant(ctx, nil, mind, w); err == nil {
			x.Wants = append(x.Wants, want)
		}
	}
	if len(x.Wants) == 0 && it.GoalID.IsZero() && it.CommitmentID.IsZero() {
		x.Gaps = append(x.Gaps, "no want, goal or commitment is recorded behind the intention")
	}
	if !it.GoalID.IsZero() && s.Goals != nil {
		if g, err := s.Goals.Get(ctx, mind, it.GoalID); err == nil {
			x.Goal = &Element{Kind: "goal", ID: string(g.ID), Text: g.Title, Detail: string(g.Status)}
		}
	}
	commitmentIDs := map[ids.ID]bool{}
	if !it.CommitmentID.IsZero() {
		commitmentIDs[it.CommitmentID] = true
	}
	for _, id := range []ids.ID{it.ID} {
		ds, _ := s.Volition.Decisions(ctx, mind, volition.DecisionFilter{IntentionID: id, Limit: 50})
		x.Decisions = append(x.Decisions, ds...)
	}
	for _, w := range x.Wants {
		ds, _ := s.Volition.Decisions(ctx, mind, volition.DecisionFilter{WantID: w.ID, Limit: 50})
		for _, d := range ds {
			if d.IntentionID.IsZero() || d.IntentionID == it.ID {
				x.Decisions = append(x.Decisions, d)
			}
		}
		if cs, err := s.Volition.Conflicts(ctx, mind, "", 200); err == nil {
			for _, c := range cs {
				other := ids.Nil
				switch w.ID {
				case c.WantA:
					other = c.WantB
				case c.WantB:
					other = c.WantA
				default:
					continue
				}
				if ow, err := s.Volition.GetWant(ctx, nil, mind, other); err == nil {
					rule, _ := c.Rationale["rule"].(string)
					outcome := c.Resolution
					if c.Preferred == w.ID {
						outcome += " in favour of this want"
					} else {
						outcome += " in favour of the other want"
					}
					x.ConflictingWants = append(x.ConflictingWants, Element{Kind: "want", ID: string(ow.ID), Text: ow.Description, Detail: outcome + ": " + rule})
				}
			}
		}
	}
	for _, d := range x.Decisions {
		if applied, ok := d.Inputs["applied"].([]any); ok {
			for _, a := range applied {
				commitmentIDs[ids.ID(fmt.Sprint(a))] = true
			}
		}
		if d.Kind == "formation" || d.Kind == "reconsideration" {
			x.Resolution = append(x.Resolution, d.Kind+" ("+d.Outcome+"): "+d.Rationale)
		}
	}
	for id := range commitmentIDs {
		if c, err := s.Volition.GetCommitment(ctx, nil, mind, id); err == nil {
			x.Commitments = append(x.Commitments, c)
		}
	}
	sort.Slice(x.Decisions, func(i, j int) bool { return x.Decisions[i].CreatedAt.Before(x.Decisions[j].CreatedAt) })
	if it.RequireApproval && x.Action != nil && x.Action.ApprovalID.IsZero() && x.Action.RequestID.IsZero() {
		x.Gaps = append(x.Gaps, "the intention required approval but no approval request is linked to this action")
	}
	return x, nil
}

func (s *Service) explainWithSubject(ctx context.Context, mind ids.ID, inner, subject Ref) (Explanation, error) {
	x, err := s.Explain(ctx, mind, inner)
	x.Subject = subject
	return x, err
}

func (s *Service) addBelief(ctx context.Context, mind ids.ID, x *Explanation, id ids.ID) {
	if s.Beliefs == nil {
		return
	}
	for _, b := range x.Beliefs {
		if b.ID == string(id) {
			return
		}
	}
	b, err := s.Beliefs.Get(ctx, nil, mind, id)
	if err != nil {
		x.Gaps = append(x.Gaps, "belief "+string(id)+" is not recorded")
		return
	}
	x.Beliefs = append(x.Beliefs, Element{Kind: "belief", ID: string(b.ID), Text: b.Proposition, Detail: fmt.Sprintf("%s, %.2f", b.Status, b.Confidence)})
	rows, _ := s.Beliefs.EvidenceFor(ctx, nil, mind, b.ID)
	for i, r := range rows {
		if i >= 5 || r.Retracted {
			continue
		}
		x.Evidence = append(x.Evidence, Element{Kind: r.Kind, ID: r.RefID, Text: fmt.Sprintf("%s %s: %s", r.Direction, firstNonEmpty(r.Kind, "evidence"), firstNonEmpty(r.Note, r.Source)), Detail: fmt.Sprintf("weight %.2f × credibility %.2f", r.Weight, r.Credibility)})
	}
}

func stepFromOrigin(origin string) ids.ID {
	if !strings.HasPrefix(origin, "plan_step:") {
		return ids.Nil
	}
	rest := strings.TrimPrefix(origin, "plan_step:")
	if i := strings.Index(rest, ":"); i > 0 {
		rest = rest[:i]
	}
	return ids.ID(rest)
}

func summaryOf(raw []byte, errText string) string {
	if errText != "" {
		return errText
	}
	s := string(raw)
	if i := strings.Index(s, `"summary":"`); i >= 0 {
		rest := s[i+11:]
		if j := strings.Index(rest, `"`); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

func short(id ids.ID) string {
	if len(id) > 8 {
		return string(id[:8])
	}
	return string(id)
}

func noteSuffix(n string) string {
	if n == "" {
		return ""
	}
	return " (\"" + n + "\")"
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---- attribution ----

// Attribution classes. They are deliberately conservative: temporal order is
// never taken for causation.
const (
	Caused      = "caused"      // a recorded causal link runs from one of the mind's actions to the outcome
	Followed    = "followed"    // the outcome came after an action of the mind on the same target, with no recorded link
	Independent = "independent" // no action of the mind on the outcome's target preceded it in the window
	UnknownAttr = "unknown"     // the records do not allow a classification
)

// Attribution classifies a mind's relation to an outcome event.
type Attribution struct {
	Outcome     ids.ID `json:"outcome_event"`
	Class       string `json:"class"`
	Reason      string `json:"reason"`
	ActionEvent ids.ID `json:"action_event,omitempty"`
	Window      string `json:"window"`
}

// AttributionWindow bounds how far back actions are considered.
const AttributionWindow = 24 * time.Hour

// Attribute classifies whether the mind caused, preceded or had nothing to
// do with an outcome, using only recorded links and records of its actions.
func (s *Service) Attribute(ctx context.Context, mind ids.ID, outcome ids.ID) (Attribution, error) {
	a := Attribution{Outcome: outcome, Window: AttributionWindow.String()}
	ev, err := s.Events.Get(ctx, outcome)
	if err != nil || ev.MindID != mind {
		return a, fmt.Errorf("event not found for this mind")
	}
	if ev.Type == events.ToolExecuted {
		a.Class, a.ActionEvent, a.Reason = Caused, ev.ID, "this event is the record of the mind's own action"
		return a, nil
	}
	chain, err := s.Events.Chain(ctx, outcome, "back", evstore.MaxChainDepth)
	if err == nil {
		for _, n := range chain.Nodes {
			if n.Event.ID != outcome && n.Event.Type == events.ToolExecuted && n.Event.MindID == mind {
				a.Class, a.ActionEvent = Caused, n.Event.ID
				a.Reason = fmt.Sprintf("a recorded link (%d hops) runs from the action %q to this outcome; the link is provenance, not proof that nothing else contributed", -n.Depth, n.Event.Summary)
				return a, nil
			}
		}
	}
	target := ""
	if t, ok := ev.Metadata["target"].(string); ok {
		target = t
	}
	actions, err := s.Events.Query(ctx, evstore.Filter{MindID: mind, Types: []events.Type{events.ToolExecuted}, Since: ev.OccurredAt.Add(-AttributionWindow), Until: ev.OccurredAt, Limit: 200})
	if err != nil {
		return a, err
	}
	outcomeTokens := strings.Fields(strings.ToLower(ev.Summary + " " + target))
	for _, act := range actions {
		t, _ := act.Metadata["target"].(string)
		t = strings.TrimSuffix(strings.ToLower(t), ".service")
		if t == "" {
			continue
		}
		for _, tok := range outcomeTokens {
			if strings.Contains(tok, t) {
				a.Class, a.ActionEvent = Followed, act.ID
				a.Reason = fmt.Sprintf("the mind acted on %s %s before this outcome, but no recorded link connects them", t, ev.OccurredAt.Sub(act.OccurredAt).Truncate(time.Second))
				return a, nil
			}
		}
	}
	if target == "" && len(actions) > 0 {
		a.Class, a.Reason = UnknownAttr, "the outcome names no target and the mind did act in the window, so the records cannot separate the two"
		return a, nil
	}
	a.Class, a.Reason = Independent, "no action of the mind on this outcome's target is recorded in the preceding "+AttributionWindow.String()
	return a, nil
}

// ---- lineage and counterfactuals ----

// Lineage is where a mind's state came from.
type Lineage struct {
	ForkedFrom    string    `json:"forked_from,omitempty"`
	SnapshotName  string    `json:"snapshot_name,omitempty"`
	SnapshotSeq   int64     `json:"snapshot_event_seq,omitempty"`
	SnapshotAt    time.Time `json:"snapshot_at,omitempty"`
	ClonedFrom    string    `json:"cloned_from,omitempty"`
	OwnSnapshots  int       `json:"own_snapshots"`
	Forks         []string  `json:"forks"`
	CreatedAt     time.Time `json:"created_at"`
	FirstEventSeq int64     `json:"first_event_seq"`
}

// LineageOf reads a mind's lineage from the minds and snapshots tables.
func (s *Service) LineageOf(ctx context.Context, mind ids.ID) (Lineage, error) {
	var l Lineage
	var forkedMind, forkedSnap, cloned *string
	if err := s.DB.QueryRow(ctx, `SELECT forked_from_mind, forked_from_snapshot, cloned_from_mind, created_at FROM minds WHERE id=$1`, mind).Scan(&forkedMind, &forkedSnap, &cloned, &l.CreatedAt); err != nil {
		return l, err
	}
	if forkedMind != nil {
		s.DB.QueryRow(ctx, `SELECT slug FROM minds WHERE id=$1`, *forkedMind).Scan(&l.ForkedFrom) //nolint:errcheck
	}
	if forkedSnap != nil {
		s.DB.QueryRow(ctx, `SELECT name, event_seq, created_at FROM snapshots WHERE id=$1`, *forkedSnap).Scan(&l.SnapshotName, &l.SnapshotSeq, &l.SnapshotAt) //nolint:errcheck
	}
	if cloned != nil {
		s.DB.QueryRow(ctx, `SELECT slug FROM minds WHERE id=$1`, *cloned).Scan(&l.ClonedFrom) //nolint:errcheck
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM snapshots WHERE mind_id=$1`, mind).Scan(&l.OwnSnapshots)                     //nolint:errcheck
	s.DB.QueryRow(ctx, `SELECT coalesce(min(seq),0) FROM cognitive_events WHERE mind_id=$1`, mind).Scan(&l.FirstEventSeq) //nolint:errcheck
	l.Forks = []string{}
	if rows, err := s.DB.Query(ctx, `SELECT slug FROM minds WHERE forked_from_mind=$1 ORDER BY created_at`, mind); err == nil {
		for rows.Next() {
			var slug string
			if rows.Scan(&slug) == nil {
				l.Forks = append(l.Forks, slug)
			}
		}
		rows.Close()
	}
	return l, nil
}

// Points renders a lineage as statements.
func (l Lineage) Points() []string {
	var p []string
	switch {
	case l.ForkedFrom != "":
		p = append(p, fmt.Sprintf("I am a fork of %s, created from its snapshot %q taken at event %d on %s.", l.ForkedFrom, l.SnapshotName, l.SnapshotSeq, l.SnapshotAt.Format("2 Jan 2006 15:04")))
		p = append(p, "My state up to that snapshot was copied; the history before it belongs to "+l.ForkedFrom+" and is shared by reference. Everything after is mine alone.")
	case l.ClonedFrom != "":
		p = append(p, "I was cloned from "+l.ClonedFrom+"'s configuration, without its state.")
	default:
		p = append(p, "I am not a fork: my history starts when I was created on "+l.CreatedAt.Format("2 Jan 2006 15:04")+".")
	}
	if l.OwnSnapshots > 0 {
		p = append(p, fmt.Sprintf("%d snapshot(s) of my own state exist; they are records of earlier states, not other minds.", l.OwnSnapshots))
	}
	if len(l.Forks) > 0 {
		p = append(p, "Minds forked from me, which have diverged since: "+strings.Join(l.Forks, ", ")+".")
	}
	return p
}

// Counterfactual is the recorded finding of a counterfactual experiment.
type Counterfactual struct {
	ExperimentID ids.ID    `json:"experiment_id"`
	Name         string    `json:"name"`
	Omitted      []string  `json:"omitted"`
	Findings     []string  `json:"findings"`
	FinishedAt   time.Time `json:"finished_at"`
}

// Counterfactuals lists completed counterfactual experiments about a mind
// whose omitted events match the text (all when text is empty).
func (s *Service) Counterfactuals(ctx context.Context, mind ids.ID, text string) ([]Counterfactual, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, name, results, coalesce(finished_at, created_at) FROM experiments
		WHERE status='done' AND definition->'protocol'->>'kind'='counterfactual' AND definition->'replay'->>'mind_id'=$1 ORDER BY finished_at DESC LIMIT 20`, string(mind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := strings.Fields(strings.ToLower(text))
	var out []Counterfactual
	for rows.Next() {
		var c Counterfactual
		var raw []byte
		if err := rows.Scan(&c.ExperimentID, &c.Name, &raw, &c.FinishedAt); err != nil {
			return nil, err
		}
		var res struct {
			Counterfactual struct {
				Omitted  []string `json:"omitted_summaries"`
				Findings []string `json:"findings"`
			} `json:"counterfactual"`
		}
		if jsonUnmarshal(raw, &res) != nil {
			continue
		}
		c.Omitted, c.Findings = res.Counterfactual.Omitted, res.Counterfactual.Findings
		if len(want) > 0 && !mentions(strings.ToLower(strings.Join(c.Omitted, " ")), want) {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func mentions(hay string, words []string) bool {
	for _, w := range words {
		if len(w) >= 5 && strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
