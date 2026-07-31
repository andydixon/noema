package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Metacognition audits the mind's own reasoning with deterministic checks
// (spec §16): insufficient evidence, single-source reliance, fluent model
// output without evidence, repeated failed hypotheses, stale contradictions
// and overconfidence against calibration. Warnings compete for attention;
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
		for subj, n := range bySubject {
			if n >= 3 {
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
	c.Runtime.Hold(workspace.Item{Key: "meta:" + check + ":" + ref, Content: t.Summary, Kind: "note", SourceEvent: ev.ID})
	return nil
}
