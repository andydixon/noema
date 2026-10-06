package cognition

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Metacognition audits the mind's own reasoning with deterministic checks
// (spec §16): insufficient evidence, single-source reliance, fluent model
// output without evidence, repeated failed hypotheses, stale contradictions,
// overconfidence against calibration, and the volition checks in
// volitionChecks. Warnings compete for attention;
// when admitted they lower confidence or open an investigation.
type Metacognition struct{}

// Name implements brain.Process.
func (Metacognition) Name() string { return "metacognition" }

// Observe implements brain.Process.
func (Metacognition) Observe(ctx context.Context, s *brain.State) ([]brain.Candidate, error) {
	if s.Services.Beliefs == nil {
		return nil, nil
	}
	var out []brain.Candidate
	seen := map[string]bool{}
	add := func(check, ref, summary string, severity float64, evidence []events.EvidenceRef, extra map[string]any) {
		key := check + ":" + ref
		if seen[key] || alreadyWarned(ctx, s, check, ref) {
			return
		}
		seen[key] = true
		content := map[string]any{"check": check, "ref": ref, "severity": severity}
		for k, v := range extra {
			content[k] = v
		}
		out = append(out, brain.Candidate{Kind: "metacognition", Subject: key, Summary: "self-check (" + check + "): " + summary, Content: content,
			Features: attention.Features{Salience: 0.3 + 0.5*severity, Consequence: 0.5 + 0.3*severity, Contradiction: 0.5 * severity, Confidence: 0.8, Novelty: 0.4}, Confidence: 0.8, Evidence: evidence})
	}
	// Beliefs touched this cycle: created or updated events in the new batch.
	for _, ev := range s.NewEvents {
		bid, _ := ev.Metadata["belief_id"].(string)
		if bid == "" || (ev.Type != events.BeliefCreated && ev.Type != events.BeliefUpdated && ev.Type != events.HypothesisSupported && ev.Type != events.HypothesisCreated) {
			continue
		}
		b, err := s.Services.Beliefs.Get(ctx, nil, s.Mind.ID, ids.ID(bid))
		if err != nil {
			continue
		}
		rows, _ := s.Services.Beliefs.EvidenceFor(ctx, nil, s.Mind.ID, b.ID)
		var live []beliefs.Evidence
		modelOnly, forCount := true, 0
		for _, r := range rows {
			if r.Retracted {
				continue
			}
			live = append(live, r.Evidence)
			if r.Direction == "for" {
				forCount++
				if r.Kind != "llm_call" && r.Kind != "note" {
					modelOnly = false
				}
			}
		}
		bd := beliefs.Compute(b.Prior, live)
		ref := []events.EvidenceRef{{Kind: "belief", ID: string(b.ID)}}
		switch {
		case b.Confidence >= 0.7 && forCount <= 1:
			add("insufficient_evidence", bid, fmt.Sprintf("%.2f confidence on one piece of evidence: %s", b.Confidence, truncate(b.Proposition, 80)), 0.6, ref, map[string]any{"belief_id": bid})
		case b.Confidence >= 0.6 && bd.SourcesFor == 1 && forCount >= 2:
			add("single_source", bid, fmt.Sprintf("all %d supporting items come from one source: %s", forCount, truncate(b.Proposition, 80)), 0.5, ref, map[string]any{"belief_id": bid})
		case forCount > 0 && modelOnly:
			add("fluent_without_evidence", bid, "belief rests only on model output: "+truncate(b.Proposition, 80), 0.7, ref, map[string]any{"belief_id": bid})
		}
	}
	// Repeated failed hypotheses on one subject.
	if rejected, err := s.Services.Beliefs.List(ctx, s.Mind.ID, beliefs.Filter{Statuses: []beliefs.Status{beliefs.StatusRejected}, Limit: 200}); err == nil {
		bySubject := map[string]int{}
		for _, b := range rejected {
			if b.Subject != "" && b.LastEvaluated.After(s.Now.Add(-30*24*time.Hour)) {
				bySubject[b.Subject]++
			}
		}
		subjects := make([]string, 0, len(bySubject))
		for subj := range bySubject {
			subjects = append(subjects, subj)
		}
		sort.Strings(subjects)
		for _, subj := range subjects {
			if n := bySubject[subj]; n >= 3 {
				add("repeated_failure", subj, fmt.Sprintf("%d hypotheses about %q were rejected this month", n, subj), 0.6, nil, map[string]any{"subject": subj, "count": n})
			}
		}
	}
	// Contradictions left open for more than a day.
	if contested, err := s.Services.Beliefs.Contested(ctx, s.Mind.ID, 20); err == nil {
		for _, b := range contested {
			if s.Now.Sub(b.LastEvaluated) > 24*time.Hour {
				add("stale_contradiction", string(b.ID), "contradiction unresolved for over a day: "+truncate(b.Proposition, 80), 0.5, []events.EvidenceRef{{Kind: "belief", ID: string(b.ID)}}, map[string]any{"belief_id": string(b.ID), "subject": b.Subject})
			}
		}
	}
	volitionChecks(ctx, s, add)
	// Overconfidence against calibration.
	if s.Services.Predictions != nil {
		if cal, err := s.Services.Predictions.Calibrate(ctx, s.Mind.ID, s.Now.Add(-90*24*time.Hour)); err == nil && cal.Overconfident {
			add("overconfidence", "calibration", fmt.Sprintf("stated confidence exceeds hit rate (accuracy %.0f%%, Brier %.2f)", cal.Accuracy*100, cal.Brier), 0.7, nil, map[string]any{"accuracy": cal.Accuracy, "brier": cal.Brier})
		}
	}
	return out, nil
}

// alreadyWarned suppresses repeats of the same check on the same target
// within a day by looking for a prior metacognition event.
func alreadyWarned(ctx context.Context, s *brain.State, check, ref string) bool {
	es, ok := s.Services.Events.(*evstore.Store)
	if !ok {
		return false
	}
	evs, err := es.Query(ctx, evstore.Filter{MindID: s.Mind.ID, Types: []events.Type{events.Metacognition}, Since: s.Now.Add(-24 * time.Hour), Limit: 200})
	if err != nil {
		return false
	}
	for _, ev := range evs {
		if ev.Metadata["check"] == check && ev.Metadata["ref"] == ref {
			return true
		}
	}
	return false
}

// metacognition is the handler: it records the warning and applies its
// effect (a small counter-evidence item or an investigation goal).
func (h Handlers) metacognition(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	check, _ := t.Content["check"].(string)
	ref, _ := t.Content["ref"].(string)
	severity, _ := t.Content["severity"].(float64)
	ev, err := c.EmitNow(ctx, events.New{Type: events.Metacognition, Source: "brain.metacognition", Summary: strings.TrimPrefix(t.Summary, "self-check "), Salience: t.Score, Confidence: 0.8, CausationID: t.EventID,
		Evidence: t.Evidence, Metadata: map[string]any{"check": check, "ref": ref, "severity": severity}, Payload: t.Content})
	if err != nil {
		return err
	}
	if bid, ok := t.Content["belief_id"].(string); ok && bid != "" && h.Beliefs != nil {
		switch check {
		case "insufficient_evidence", "single_source", "fluent_without_evidence":
			_, err := h.Beliefs.AddEvidence(ctx, c.Tx, emitter(c), c.State.Mind.ID, ids.ID(bid), beliefs.EvidenceInput{Direction: "against", Kind: "metacognition", RefID: string(ev.ID), Source: "metacognition:" + check, Weight: 0.15 + 0.15*severity, Credibility: 0.9, Note: "self-check: " + check, Derivation: beliefs.DerivationReflection, Family: "metacognition"}, "metacognitive discount: "+check, ev.ID, c.State.CycleID)
			if err != nil {
				return err
			}
		case "stale_contradiction":
			if c.State.Services.Goals != nil {
				subj, _ := t.Content["subject"].(string)
				if _, err := c.State.Services.Goals.FindBySubject(ctx, c.Tx, c.State.Mind.ID, "investigation", "contradiction:"+bid); err != nil {
					if g, err := c.State.Services.Goals.Create(ctx, c.Tx, goalNew(c.State.Mind.ID, "Resolve the contradiction about "+firstNonEmpty(subj, "this belief"), "contradiction:"+bid, 0.6, ev.ID)); err == nil {
						c.Emit(events.New{Type: events.GoalCreated, Source: "brain.metacognition", Summary: "investigation goal: " + g.Title, Salience: 0.5, Confidence: 0.8, CausationID: ev.ID, Evidence: []events.EvidenceRef{{Kind: "goal", ID: string(g.ID)}}, Metadata: map[string]any{"goal_id": string(g.ID)}})
					}
				}
			}
		}
	}
	if check == "intention_outlived_goal" && c.State.Services.Volition != nil {
		if iid, ok := t.Content["intention_id"].(string); ok {
			r := recorder(c)
			r.Cause = ev.ID
			if _, err := c.State.Services.Volition.Apply(ctx, r, c.State.Mind.ID, ids.ID(iid), volition.Reconsideration{Outcome: volition.Abandon, Reason: "the goal it served is no longer open"}, volition.Trigger{Kind: volition.TriggerGoalCompleted, EventID: ev.ID}); err != nil && !errors.Is(err, volition.ErrClosed) {
				return err
			}
		}
	}
	c.Runtime.Hold(workspace.Item{Key: "meta:" + check + ":" + ref, Content: t.Summary, Kind: "note", SourceEvent: ev.ID})
	return nil
}

// volitionChecks audit wanting, intending and acting: strong wants that keep
// being suppressed, intentions with no recorded reason, intentions a live
// commitment forbids, commitments that cannot be kept, churn, repeated
// failed execution, plans whose cost outweighs the intention's value, and
// intentions that outlived their goal.
func volitionChecks(ctx context.Context, s *brain.State, add func(check, ref, summary string, severity float64, evidence []events.EvidenceRef, extra map[string]any)) {
	vs := s.Services.Volition
	if vs == nil {
		return
	}
	if ws, err := vs.Wants(ctx, nil, s.Mind.ID, []volition.WantStatus{volition.WantActive}, 300); err == nil {
		for _, w := range ws {
			if w.Suppressions >= 3 && w.Effective(s.Now) >= 0.5 {
				add("want_suppressed_repeatedly", string(w.ID), fmt.Sprintf("a strong want (%.2f) has been set aside %d times: %s", w.Effective(s.Now), w.Suppressions, truncate(w.Description, 80)), 0.5,
					[]events.EvidenceRef{{Kind: "want", ID: string(w.ID)}}, map[string]any{"want_id": string(w.ID)})
			}
		}
	}
	open, err := vs.Open(ctx, nil, s.Mind.ID)
	if err != nil {
		return
	}
	commitments, _ := vs.Commitments(ctx, nil, s.Mind.ID, []string{"active"}, 500)
	goalStatus := map[ids.ID]string{}
	if s.Services.Goals != nil {
		if gs, err := s.Services.Goals.List(ctx, s.Mind.ID, nil, 500); err == nil {
			for _, g := range gs {
				goalStatus[g.ID] = string(g.Status)
			}
		}
	}
	for _, it := range open {
		ref := []events.EvidenceRef{{Kind: "intention", ID: string(it.ID)}}
		if len(it.SourceWants) == 0 && it.GoalID.IsZero() && it.CommitmentID.IsZero() && it.ParentID.IsZero() {
			add("intention_unsupported", string(it.ID), "an intention has no recorded want, goal or commitment behind it: "+truncate(it.Description, 80), 0.6, ref, map[string]any{"intention_id": string(it.ID)})
		}
		if a := ActionInfo(ctx, s.Services.Capabilities, s.Mind.ID, it.Proposal); a != nil && !s.Mind.Config.Ablated("commitments") {
			if chk := volition.CheckAction(commitments, *a, s.Now); len(chk.Forbidden) > 0 {
				add("intention_conflicts_commitment", string(it.ID), fmt.Sprintf("intention %q conflicts with commitment %q", truncate(it.Description, 60), truncate(chk.Forbidden[0].Description, 60)), 0.7,
					append(ref, events.EvidenceRef{Kind: "commitment", ID: string(chk.Forbidden[0].ID)}), map[string]any{"intention_id": string(it.ID), "commitment_id": string(chk.Forbidden[0].ID)})
			}
		}
		if st, ok := goalStatus[it.GoalID]; ok && (st == "completed" || st == "abandoned") {
			add("intention_outlived_goal", string(it.ID), "intention still open although its goal is "+st+": "+truncate(it.Description, 80), 0.5, ref, map[string]any{"intention_id": string(it.ID)})
		}
		if !it.PlanID.IsZero() && s.Services.Plans != nil {
			if p, err := s.Services.Plans.Get(ctx, nil, it.PlanID); err == nil && planning.PlanOpen(p.Status) && it.Priority < 0.4 && (p.Estimate.Approvals >= 3 || p.Estimate.Invocations > p.Budget.MaxInvocations) {
				add("cost_exceeds_value", string(p.ID), fmt.Sprintf("plan for a low-priority intention (%.2f) needs %d approvals and %d invocations: %s", it.Priority, p.Estimate.Approvals, p.Estimate.Invocations, truncate(it.Description, 60)), 0.4,
					append(ref, events.EvidenceRef{Kind: "plan", ID: string(p.ID)}), map[string]any{"intention_id": string(it.ID), "plan_id": string(p.ID)})
			}
		}
	}
	if s.Services.Capabilities != nil {
		effects := map[string]string{}
		if inv, err := s.Services.Capabilities.Inventory(ctx, s.Mind.ID); err == nil {
			for _, i := range inv {
				effects[i.Capability.Name] = string(i.Effect)
				if i.Capability.Locked {
					effects[i.Capability.Name] = "locked"
				}
			}
		}
		for _, cm := range commitments {
			if why := volition.Impossible(cm, effects, s.Now); why != "" {
				add("commitment_impossible", string(cm.ID), "commitment cannot be kept: "+truncate(cm.Description, 60)+" — "+why, 0.7, []events.EvidenceRef{{Kind: "commitment", ID: string(cm.ID)}}, map[string]any{"commitment_id": string(cm.ID)})
			}
		}
	}
	es, ok := s.Services.Events.(*evstore.Store)
	if !ok {
		return
	}
	if evs, err := es.Query(ctx, evstore.Filter{MindID: s.Mind.ID, Types: []events.Type{events.IntentionReconsidered, events.IntentionAbandoned, events.IntentionSuperseded, events.IntentionSuspended}, Since: s.Now.Add(-time.Hour), Limit: 200}); err == nil && len(evs) >= 10 {
		add("intention_churn", "hour:"+s.Now.Format("2006-01-02T15"), fmt.Sprintf("%d intention changes in the last hour", len(evs)), 0.5, nil, map[string]any{"count": len(evs)})
	}
	if evs, err := es.Query(ctx, evstore.Filter{MindID: s.Mind.ID, Types: []events.Type{events.PlanStepFailed}, Since: s.Now.Add(-24 * time.Hour), Limit: 300}); err == nil {
		byAction := map[string]int{}
		for _, ev := range evs {
			if capName, _ := ev.Metadata["capability"].(string); capName != "" {
				byAction[capName]++
			}
		}
		names := make([]string, 0, len(byAction))
		for capName := range byAction {
			names = append(names, capName)
		}
		sort.Strings(names) // candidates in a stable order, for replay
		for _, capName := range names {
			if n := byAction[capName]; n >= 3 {
				add("repeated_execution_failure", capName, fmt.Sprintf("%s failed %d times in plans today", capName, n), 0.6, nil, map[string]any{"capability": capName, "count": n})
			}
		}
	}
}
