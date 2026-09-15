package cognition

import (
	"context"
	"encoding/json"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Act turns explicit action requests in conversation ("fetch <url>",
// "restart <unit>", "logs for <unit>") into wants carrying the proposed
// means. It no longer invokes anything: whether the mind intends to act,
// under which commitments and with what plan, is decided by formation, and
// acting goes through the capability layer, so a request never bypasses
// ALLOW / ASK / DENY.
type Act struct{}

// Name implements brain.Process.
func (Act) Name() string { return "act" }

// Observe implements brain.Process.
func (Act) Observe(_ context.Context, s *brain.State) ([]brain.Candidate, error) {
	if s.Services.Volition == nil {
		return nil, nil
	}
	var out []brain.Candidate
	for _, ev := range s.NewEvents {
		if ev.Type != events.Observation || ev.Metadata["conversation_id"] == nil {
			continue
		}
		var p events.ObservationPayload
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		if conversation.Analyse(p.Text).Intent != "request" {
			continue
		}
		prop := volition.ProposalFromText(p.Text, "")
		if prop == nil || prop.Kind != "capability" {
			continue
		}
		out = append(out, brain.Candidate{Kind: "want", Subject: prop.Capability + ":" + prop.Target, Summary: "wants to " + prop.Capability + " " + prop.Target + " as " + firstNonEmpty(p.Actor, "someone") + " asked",
			Content:    map[string]any{"proposal": *prop, "text": truncate(p.Text, 200), "actor": p.Actor, "operator": p.Trust == events.TrustOperator, "source_event": string(ev.ID), "conversation_id": ev.Metadata["conversation_id"]},
			Features:   attention.Features{Salience: 0.7, Urgency: 0.6, Consequence: 0.7, Confidence: 0.9, OperatorPriority: prioFor(p.Trust), ContextRelevance: 0.5},
			Confidence: 0.9, SourceEvents: []ids.ID{ev.ID}, Evidence: []events.EvidenceRef{{Kind: "event", ID: string(ev.ID), Note: "request"}}})
	}
	return out, nil
}

func prioFor(t events.Trust) float64 {
	if t == events.TrustOperator {
		return 1
	}
	return 0.7
}
