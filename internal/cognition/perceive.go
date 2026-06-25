package cognition

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Perceive understands conversational messages. The deterministic analysis
// always runs; when a model is available and the cycle budget allows, the
// perceive prompt enriches entities and claims. Someone is waiting for an
// answer, so the candidate carries high urgency and operator priority.
type Perceive struct{}

// Name implements brain.Process.
func (Perceive) Name() string { return "perceive" }

// Observe implements brain.Process.
func (Perceive) Observe(ctx context.Context, s *brain.State) ([]brain.Candidate, error) {
	var out []brain.Candidate
	for _, ev := range s.NewEvents {
		if ev.Type != events.Observation || ev.Metadata["conversation_id"] == nil {
			continue
		}
		var p events.ObservationPayload
		if json.Unmarshal(ev.Payload, &p) != nil || p.Text == "" {
			continue
		}
		a := conversation.Analyse(p.Text)
		content := map[string]any{"source_event": string(ev.ID), "conversation_id": ev.Metadata["conversation_id"], "message_id": ev.Metadata["message_id"], "text": p.Text, "actor": p.Actor, "trust": p.Trust}
		evidence := []events.EvidenceRef{{Kind: "event", ID: string(ev.ID), Note: "message"}}
		if s.Services.LLM != nil && s.Services.Prompts != nil && s.Services.LLM.Available(ctx, llm.RolePerceive) && s.AllowLLM() {
			if pr, err := s.Services.Prompts.Get(ctx, "perceive.message"); err == nil {
				if req, err := pr.Render(map[string]any{"Text": p.Text, "Actor": p.Actor, "Trust": p.Trust, "Channel": p.Channel}); err == nil {
					req.MindID, req.CorrelationID, req.Purpose = s.Mind.ID, s.CycleID, "perceive message"
					if resp, err := s.Services.LLM.Complete(ctx, req); err == nil && len(resp.JSON) > 0 {
						merge(&a, resp.JSON)
						content["llm_call"] = string(resp.CallID)
						content["llm_model"] = resp.Provider + "/" + resp.Model
						evidence = append(evidence, events.EvidenceRef{Kind: "llm_call", ID: string(resp.CallID), Note: "perception"})
					}
				}
			}
		}
		content["analysis"] = a
		prio := 0.7
		if p.Trust == events.TrustOperator {
			prio = 1
		}
		out = append(out, brain.Candidate{Kind: "perceive", Subject: string(ev.ID), Summary: "understand message (" + a.Intent + "): " + truncate(p.Text, 100), Content: content,
			Features:   attention.Features{Salience: 0.9, Urgency: 0.6 + 0.4*a.Urgency, Novelty: 0.5, Consequence: 0.8, Confidence: 1, OperatorPriority: prio, ContextRelevance: 0.6},
			Confidence: 1, SourceEvents: []ids.ID{ev.ID}, Evidence: evidence})
	}
	return out, nil
}

// merge folds model-extracted fields into the deterministic analysis. The
// intent stays deterministic unless the model disagrees on question/statement
// and the message has no question mark; entities and claims are unioned.
func merge(a *conversation.Analysis, raw json.RawMessage) {
	var m struct {
		Intent    string                                    `json:"intent"`
		Summary   string                                    `json:"summary"`
		Entities  []struct{ Name, Kind string }             `json:"entities"`
		Claims    []struct{ Proposition, Certainty string } `json:"claims"`
		Questions []string                                  `json:"questions"`
		Urgency   float64                                   `json:"urgency"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	a.Source = "llm"
	seen := map[string]bool{}
	for _, e := range a.Entities {
		seen[strings.ToLower(e)] = true
	}
	for _, e := range m.Entities {
		if e.Name != "" && !seen[strings.ToLower(e.Name)] {
			seen[strings.ToLower(e.Name)] = true
			a.Entities = append(a.Entities, e.Name)
		}
	}
	if a.Intent == "statement" || a.Intent == "correction" {
		known := map[string]bool{}
		for _, c := range a.Claims {
			known[strings.ToLower(c.Proposition)] = true
		}
		for _, c := range m.Claims {
			if c.Proposition != "" && !known[strings.ToLower(c.Proposition)] && c.Certainty != "asked" {
				cert := "asserted"
				if c.Certainty == "hedged" {
					cert = "hedged"
				}
				a.Claims = append(a.Claims, conversation.Claim{Proposition: c.Proposition, Certainty: cert})
			}
		}
	}
	if m.Urgency > a.Urgency {
		a.Urgency = m.Urgency
	}
	if a.Intent == "statement" && m.Intent == "request" {
		a.Intent = "request"
	}
}
