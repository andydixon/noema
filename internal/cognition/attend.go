// Package cognition holds the built-in cognitive processes. Each is a small,
// mostly deterministic Process; LLM-assisted ones arrive with the provider
// layer and always degrade to deterministic behaviour when no model is
// available (spec §46, §62).
package cognition

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Attend turns each new external event into an "attend" candidate whose
// features are computed from the event itself and the current context.
type Attend struct{}

// Name implements brain.Process.
func (Attend) Name() string { return "attend" }

// urgent words raise urgency deterministically; a model may refine this later
// but the floor is always computed here.
var urgentWords = map[string]float64{"error": 0.7, "errors": 0.7, "fail": 0.8, "failed": 0.8, "failure": 0.8, "down": 0.8, "outage": 0.9, "alert": 0.8, "alarm": 0.8,
	"critical": 0.9, "urgent": 0.9, "immediately": 0.8, "crash": 0.8, "crashed": 0.8, "timeout": 0.6, "latency": 0.5, "spike": 0.6, "smart": 0.4, "corrupt": 0.8, "lost": 0.6}

// Observe implements brain.Process.
func (Attend) Observe(_ context.Context, s *brain.State) ([]brain.Candidate, error) {
	ctxTokens := s.Workspace.Tokens()
	var out []brain.Candidate
	for _, ev := range s.NewEvents {
		if strings.HasPrefix(ev.Source, "brain") {
			continue
		}
		switch ev.Type {
		case events.Observation, events.Perception, events.OperatorAction, events.ToolExecuted, events.ToolFailed, events.PredictionError, events.ContradictionDetected:
		default:
			continue
		}
		text := ev.Summary
		var trust events.Trust
		if ev.Type == events.Observation {
			var p events.ObservationPayload
			if json.Unmarshal(ev.Payload, &p) == nil {
				if p.Text != "" {
					text = p.Text
				}
				trust = p.Trust
			}
		}
		toks := workspace.Tokenise(text)
		f := attention.Features{Salience: ev.Salience, Confidence: ev.Confidence, Consequence: 0.4}
		f.Urgency = urgency(toks)
		f.ContextRelevance = workspace.Overlap(toks, ctxTokens)
		f.GoalRelevance, _ = goals.Relevance(text, s.Goals)
		f.Novelty = 1 - f.ContextRelevance*0.7 // things we are already holding are less novel
		switch trust {
		case events.TrustOperator:
			f.OperatorPriority = 1
		case events.TrustUser:
			f.OperatorPriority = 0.7
		default:
			f.OperatorPriority = 0.3
		}
		if ev.Type == events.OperatorAction {
			f.OperatorPriority, f.Salience = 0.9, max(ev.Salience, 0.5)
		}
		if ev.Type == events.ContradictionDetected {
			f.Contradiction = 1
		}
		if ev.Type == events.PredictionError {
			f.Consequence = 0.8
		}
		out = append(out, brain.Candidate{Kind: "attend", Subject: string(ev.ID), Summary: ev.Summary,
			Content:  map[string]any{"source_event": string(ev.ID), "event_type": ev.Type, "text": truncate(text, 2000), "trust": trust},
			Features: f, Confidence: ev.Confidence, SourceEvents: []ids.ID{ev.ID}, Evidence: []events.EvidenceRef{{Kind: "event", ID: string(ev.ID)}}})
	}
	return out, nil
}

func urgency(tokens []string) float64 {
	var u float64
	for _, t := range tokens {
		if v, ok := urgentWords[t]; ok && v > u {
			u = v
		}
	}
	if u == 0 {
		return 0.2
	}
	return u
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
