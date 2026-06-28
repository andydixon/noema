package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/narrative"
	"github.com/andydixon/noema/pkg/events"
)

// NarrativeHook rebuilds the internal narrative after cycles that changed
// the workspace. The structure is computed from state; a model may rephrase
// it when the mind's configuration allows and the budget permits.
type NarrativeHook struct {
	Store *narrative.Store
}

// Name implements brain.Hook.
func (NarrativeHook) Name() string { return "narrative" }

// AfterCycle implements brain.Hook.
func (h NarrativeHook) AfterCycle(ctx context.Context, c *brain.Cycle, emitted []events.Event) error {
	if h.Store == nil {
		return nil
	}
	ws := c.Runtime.Workspace()
	st := narrative.Structured{Mode: string(c.State.Mode), Cycle: c.State.Cycle, GeneratedAt: c.State.Now}
	for i, t := range ws.Workspace {
		if i >= 3 {
			break
		}
		st.Doing = append(st.Doing, doingLabel(t.Kind, t.Summary))
	}
	for _, ev := range c.State.NewEvents {
		if ev.Type == events.Observation && len(st.Why) < 2 {
			st.Why = append(st.Why, "of: "+oneLine(truncate(ev.Summary, 80)))
		}
	}
	for _, ev := range emitted {
		switch ev.Type {
		case events.BeliefCreated, events.HypothesisCreated, events.ContradictionDetected, events.PredictionError, events.MemoryConsolidated, events.GoalCreated, events.ResponseGenerated:
			if len(st.Recently) < 4 {
				st.Recently = append(st.Recently, oneLine(truncate(ev.Summary, 90)))
			}
		}
	}
	if c.State.Services.Beliefs != nil {
		if contested, _ := c.State.Services.Beliefs.Contested(ctx, c.State.Mind.ID, 3); len(contested) > 0 {
			for _, b := range contested {
				st.Resolving = append(st.Resolving, "contradiction: "+truncate(b.Proposition, 70))
			}
		}
	}
	for _, g := range c.State.Goals {
		if g.Status == goals.StatusActive && len(st.Resolving) < 5 {
			st.Resolving = append(st.Resolving, "goal: "+truncate(g.Title, 70))
		}
	}
	if c.State.Services.Predictions != nil {
		if open, _ := c.State.Services.Predictions.Open(ctx, c.State.Mind.ID); len(open) > 0 {
			for i, p := range open {
				if i >= 2 {
					break
				}
				st.Next = append(st.Next, fmt.Sprintf("expect (%.2f): %s", p.Confidence, truncate(p.Statement, 70)))
			}
		}
	}
	old, _ := h.Store.Get(ctx, c.State.Mind.ID)
	if !narrative.Changed(old.Structured, st) && time.Since(old.UpdatedAt) < 10*time.Minute {
		return nil
	}
	text := narrative.Render(st)
	if c.State.Mind.Config.NarrativeLLM && c.State.Services.LLM != nil && c.State.Services.Prompts != nil && c.State.Services.LLM.Available(ctx, llm.RoleNarrate) && c.State.AllowLLM() {
		if pr, err := c.State.Services.Prompts.Get(ctx, "narrate"); err == nil {
			if req, err := pr.Render(map[string]any{"Workspace": st.Doing, "Recent": st.Recently, "Goals": st.Resolving, "Contradictions": len(st.Resolving)}); err == nil {
				req.MindID, req.CorrelationID, req.Purpose = c.State.Mind.ID, c.State.CycleID, "narrative"
				if resp, err := c.State.Services.LLM.Complete(ctx, req); err == nil && strings.TrimSpace(resp.Text) != "" {
					text = strings.TrimSpace(resp.Text)
				}
			}
		}
	}
	ev, err := c.EmitNow(ctx, events.New{Type: events.NarrativeUpdated, Source: "brain.narrative", Summary: truncate(strings.SplitN(text, "\n", 2)[0], 160), Salience: 0.15, Confidence: 0.9,
		Metadata: map[string]any{"cycle": c.State.Cycle}, Payload: map[string]any{"text": text, "structured": st}})
	if err != nil {
		return err
	}
	return h.Store.Save(ctx, c.Tx, c.State.Mind.ID, text, st, ev.ID)
}

// doingLabel phrases a workspace thought as an activity.
func doingLabel(kind, summary string) string {
	summary = oneLine(summary)
	switch kind {
	case "perceive":
		if i := strings.Index(summary, "): "); i > 0 && strings.HasPrefix(summary, "understand message (") {
			return "reading a message: " + summary[i+3:]
		}
		return "reading a message"
	case "attend":
		return "attending to: " + summary
	case "retrieve":
		return "recalling: " + strings.TrimPrefix(summary, "recalled: ")
	case "hypothesis":
		return "weighing a " + summary
	case "question":
		return "wondering: " + strings.TrimPrefix(summary, "question: ")
	case "goal":
		return "working on: " + summary
	case "metacognition":
		return "checking myself: " + strings.TrimPrefix(summary, "self-check ")
	}
	return summary
}

// oneLine collapses whitespace so narrative items stay single lines.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
