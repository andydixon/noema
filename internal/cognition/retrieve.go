package cognition

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Retrieve recalls memories relevant to each new external event and proposes
// them as candidates. Retrieval scoring is deterministic and recorded on the
// candidate so the UI can show why a memory came to mind.
type Retrieve struct{}

// Name implements brain.Process.
func (Retrieve) Name() string { return "retrieve" }

// minRetrievalScore filters weak matches before they compete for attention.
const minRetrievalScore = 0.30

// Observe implements brain.Process.
func (Retrieve) Observe(ctx context.Context, s *brain.State) ([]brain.Candidate, error) {
	if s.Services.Memory == nil {
		return nil, nil
	}
	var out []brain.Candidate
	seen := map[ids.ID]bool{}
	for _, ev := range s.NewEvents {
		if strings.HasPrefix(ev.Source, "brain") || (ev.Type != events.Observation && ev.Type != events.Perception) {
			continue
		}
		text := ev.Summary
		var p events.ObservationPayload
		if json.Unmarshal(ev.Payload, &p) == nil && p.Text != "" {
			text = p.Text
		}
		hits, err := s.Services.Memory.Retrieve(ctx, s.Mind.ID, memory.Query{Text: text, Limit: 5, Now: s.Now, Semantic: true})
		if err != nil {
			return out, err
		}
		for _, h := range hits {
			m := h.Memory
			if h.Score < minRetrievalScore || m.OriginEvent == ev.ID || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			out = append(out, brain.Candidate{Kind: "retrieve", Subject: string(m.ID), Summary: "recalled: " + truncate(m.Content, 160),
				Content:    map[string]any{"ref": string(m.ID), "memory_kind": m.Kind, "score": h.Score, "components": h.Components, "query": truncate(text, 200), "source_event": string(ev.ID)},
				Features:   attention.Features{Salience: h.Score * 0.8, ContextRelevance: h.Score, Confidence: m.Confidence, Novelty: 0.2, Consequence: 0.3 + 0.4*m.Importance, Urgency: 0.1},
				Confidence: m.Confidence, SourceEvents: []ids.ID{ev.ID},
				Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(m.ID), Note: "retrieved"}, {Kind: "event", ID: string(ev.ID), Note: "cue"}}})
		}
	}
	return out, nil
}
