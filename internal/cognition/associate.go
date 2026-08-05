package cognition

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/epistemics"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Associate proposes low-confidence causal hypotheses when two notable
// observations arrive close together ("deployment at 09:10, latency at
// 09:12"). It is deliberately weak: confidence comes from the belief layer's
// arithmetic over the two observations, never from the wording.
type Associate struct {
	Window time.Duration // how far back an earlier observation may lie
}

// Name implements brain.Process.
func (Associate) Name() string { return "associate" }

// Observe implements brain.Process.
func (a Associate) Observe(_ context.Context, s *brain.State) ([]brain.Candidate, error) {
	if s.Services.Beliefs == nil {
		return nil, nil
	}
	window := a.Window
	if window <= 0 {
		window = 15 * time.Minute
	}
	var out []brain.Candidate
	for _, ev := range s.NewEvents {
		if strings.HasPrefix(ev.Source, "brain") || ev.Type != events.Observation {
			continue
		}
		laterToks := workspace.Tokenise(ev.Summary)
		laterU := urgency(laterToks)
		if laterU < 0.4 {
			continue
		}
		// Speech is not a world event. A hypothetical or a question cannot be a
		// cause or an effect, and two turns by the same speaker in one
		// conversation are one person talking, not a sequence of happenings.
		speaker := ""
		if ev.Metadata["conversation_id"] != nil {
			var p events.ObservationPayload
			if json.Unmarshal(ev.Payload, &p) == nil {
				speaker = p.Actor
			}
			if !worldly(speaker, ev.Summary) {
				continue
			}
		}
		for _, it := range s.Workspace.WorkingMemory {
			if it.Kind != "observation" || it.SourceEvent.IsZero() || it.SourceEvent == ev.ID {
				continue
			}
			if speaker != "" && strings.HasPrefix(it.Content, speaker+": ") {
				continue
			}
			if !worldly("", it.Content) {
				continue
			}
			if ev.OccurredAt.Sub(it.TouchedAt) > window || it.TouchedAt.After(ev.OccurredAt) {
				continue
			}
			earlierToks := workspace.Tokenise(it.Content)
			if urgency(earlierToks) < 0.3 && workspace.Overlap(earlierToks, toSet(laterToks)) < 0.2 {
				continue
			}
			pair := []string{string(it.SourceEvent), string(ev.ID)}
			sort.Strings(pair)
			prop := truncate(it.Content, 100) + " may have caused: " + truncate(ev.Summary, 100)
			out = append(out, brain.Candidate{Kind: "hypothesis", Subject: strings.Join(pair, "+"), Summary: "hypothesis: " + prop,
				Content:    map[string]any{"proposition": prop, "cause_event": string(it.SourceEvent), "effect_event": string(ev.ID), "gap_seconds": int(ev.OccurredAt.Sub(it.TouchedAt).Seconds()), "source_event": string(ev.ID)},
				Features:   attention.Features{Salience: 0.4 + 0.5*ev.Salience, Novelty: 0.85, Consequence: 0.8, Urgency: laterU * 0.6, Confidence: 0.3, ContextRelevance: 0.6, OperatorPriority: 0.5},
				Confidence: 0.3, SourceEvents: []ids.ID{ev.ID, it.SourceEvent},
				Evidence: []events.EvidenceRef{{Kind: "event", ID: string(it.SourceEvent), Note: "earlier"}, {Kind: "event", ID: string(ev.ID), Note: "later"}}})
		}
	}
	return out, nil
}

func toSet(ws []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ws))
	for _, w := range ws {
		m[w] = struct{}{}
	}
	return m
}

// worldly reports whether an utterance describes something that happened,
// as opposed to a hypothetical, a question or a request, which cannot stand
// as a cause or an effect.
func worldly(speaker, summary string) bool {
	text := summary
	if speaker != "" {
		text = strings.TrimPrefix(text, speaker+": ")
	} else if i := strings.Index(text, ": "); i > 0 && i < 40 {
		text = text[i+2:]
	}
	in := epistemics.Interpret(speaker, text)
	switch in.Modality {
	case epistemics.ModalityHypothetical, epistemics.ModalityQuestion, epistemics.ModalityImperative:
		return false
	}
	return true
}
