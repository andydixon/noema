package cognition

import (
	"context"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// GoalMonitor watches the goal tree: stalled goals are raised for attention,
// and goals whose subject appears in new events get a progress nudge
// candidate. It never completes a goal on its own; that needs evidence or
// an operator.
type GoalMonitor struct {
	StallAfter time.Duration
}

// Name implements brain.Process.
func (GoalMonitor) Name() string { return "goal-monitor" }

// Observe implements brain.Process.
func (g GoalMonitor) Observe(ctx context.Context, s *brain.State) ([]brain.Candidate, error) {
	if s.Services.Goals == nil {
		return nil, nil
	}
	stall := g.StallAfter
	if stall <= 0 {
		stall = 24 * time.Hour
	}
	var out []brain.Candidate
	stalled, _ := s.Services.Goals.Stalled(ctx, s.Mind.ID, stall)
	for i, gl := range stalled {
		if i >= 3 {
			break
		}
		out = append(out, brain.Candidate{Kind: "goal", Subject: string(gl.ID), Summary: "goal stalled: " + gl.Title,
			Content:  map[string]any{"goal_id": string(gl.ID), "reason": "no progress for " + stall.String(), "ref": string(gl.ID), "stalled": true},
			Features: attention.Features{Salience: 0.4, GoalRelevance: gl.Priority, Consequence: 0.5 * gl.Priority, Urgency: 0.3, Confidence: 1}, Confidence: 1,
			Evidence: []events.EvidenceRef{{Kind: "goal", ID: string(gl.ID)}}})
	}
	// New external events touching a goal's subject.
	for _, ev := range s.NewEvents {
		if strings.HasPrefix(ev.Source, "brain") || ev.Type != events.Observation {
			continue
		}
		if rel, id := goals.Relevance(ev.Summary, s.Goals); rel >= 0.5 && !id.IsZero() {
			out = append(out, brain.Candidate{Kind: "goal", Subject: string(id) + ":" + string(ev.ID), Summary: "evidence for a goal: " + truncate(ev.Summary, 100),
				Content:  map[string]any{"goal_id": string(id), "reason": "new observation relevant to the goal", "source_event": string(ev.ID), "relevance": rel, "ref": string(id)},
				Features: attention.Features{Salience: ev.Salience * 0.7, GoalRelevance: rel, Consequence: 0.4, Confidence: 1}, Confidence: 1, SourceEvents: []ids.ID{ev.ID},
				Evidence: []events.EvidenceRef{{Kind: "goal", ID: string(id)}, {Kind: "event", ID: string(ev.ID)}}})
		}
	}
	return out, nil
}

// goal handles admitted goal thoughts: holds the goal in working memory and
// records that it was attended (progress nudges are logged, not invented).
func (h Handlers) goal(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	gid, _ := t.Content["goal_id"].(string)
	reason, _ := t.Content["reason"].(string)
	src, _ := t.Content["source_event"].(string)
	c.Runtime.Hold(workspace.Item{Key: "goal:" + gid, Content: t.Summary, Kind: "goal", Ref: gid, SourceEvent: ids.ID(src)})
	c.Emit(events.New{Type: events.GoalUpdated, Source: "brain.goals", Summary: t.Summary + " (" + reason + ")", Salience: t.Score, Confidence: 1, CausationID: t.EventID,
		Evidence: t.Evidence, Metadata: map[string]any{"goal_id": gid, "attended": true, "stalled": t.Content["stalled"] == true}, Payload: map[string]any{"goal_id": gid, "reason": reason}})
	return nil
}

// question handles admitted curiosity questions: an investigation goal is
// created (once per subject) so the question persists beyond the workspace.
func (h Handlers) question(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	if c.State.Services.Goals == nil {
		return nil
	}
	subject, _ := t.Content["subject"].(string)
	question, _ := t.Content["question"].(string)
	src, _ := t.Content["source_event"].(string)
	if existing, err := c.State.Services.Goals.FindBySubject(ctx, c.Tx, c.State.Mind.ID, goals.KindInvestigation, subject); err == nil {
		c.Runtime.Hold(workspace.Item{Key: "goal:" + string(existing.ID), Content: existing.Title, Kind: "goal", Ref: string(existing.ID)})
		return nil
	}
	prio := 0.3 + 0.4*c.State.Motiv.Affect.Curiosity
	g, err := c.State.Services.Goals.Create(ctx, c.Tx, goals.New{MindID: c.State.Mind.ID, Title: question, Kind: goals.KindInvestigation, Priority: prio, Subject: subject, Owner: "mind", OriginEvent: t.EventID})
	if err != nil {
		return err
	}
	c.Emit(events.New{Type: events.GoalCreated, Source: "brain.curiosity", Summary: "curious: " + question, Salience: 0.4 + 0.3*prio, Confidence: 0.6, CausationID: t.EventID, ParentIDs: []ids.ID{t.EventID, ids.ID(src)},
		Evidence: append(t.Evidence, events.EvidenceRef{Kind: "goal", ID: string(g.ID)}), Metadata: map[string]any{"goal_id": string(g.ID), "kind": g.Kind, "priority": prio}, Payload: g})
	c.Runtime.Hold(workspace.Item{Key: "goal:" + string(g.ID), Content: "investigating: " + question, Kind: "goal", Ref: string(g.ID), SourceEvent: ids.ID(src)})
	return nil
}
