package cognition

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Curiosity proposes questions about unexplained observations, prediction
// errors and contradictions. Its output is questions and investigation goals
// only; it never grants or requests permission to act (spec §12).
type Curiosity struct{}

// Name implements brain.Process.
func (Curiosity) Name() string { return "curiosity" }

// Observe implements brain.Process.
func (Curiosity) Observe(ctx context.Context, s *brain.State) ([]brain.Candidate, error) {
	drive := 0.5
	for _, d := range s.Motiv.Drives {
		if d.Name == "curiosity" {
			drive = d.Value
		}
	}
	var out []brain.Candidate
	for _, ev := range s.NewEvents {
		if strings.HasPrefix(ev.Source, "brain") && ev.Type != events.PredictionError && ev.Type != events.ContradictionDetected {
			continue
		}
		switch ev.Type {
		case events.PredictionError:
			stmt, _ := payloadField(ev, "statement")
			q := "Why was my prediction wrong: " + truncate(stmt, 120) + "?"
			out = append(out, curiosityCandidate(ev, q, "prediction:"+ev.Metadata["prediction_id"].(string), 0.8, drive))
		case events.ContradictionDetected:
			q := "Which is right: " + truncate(ev.Summary, 140) + "?"
			out = append(out, curiosityCandidate(ev, q, "contradiction:"+fmtID(ev.Metadata["belief_id"])+"+"+fmtID(ev.Metadata["other_id"]), 0.7, drive))
		case events.Observation:
			if ev.Metadata["conversation_id"] != nil || ev.Salience < 0.5 {
				continue
			}
			text := ev.Summary
			var p events.ObservationPayload
			if json.Unmarshal(ev.Payload, &p) == nil && p.Text != "" {
				text = p.Text
			}
			if urgency(tokenise(text)) < 0.5 {
				continue
			}
			// Unexplained: no belief and no memory speaks to it.
			explained := false
			if s.Services.Beliefs != nil {
				if bs, _ := s.Services.Beliefs.Search(ctx, s.Mind.ID, text, 3); hasLive(bs) {
					explained = true
				}
			}
			if !explained && s.Services.Memory != nil {
				if hits, _ := s.Services.Memory.Retrieve(ctx, s.Mind.ID, memory.Query{Text: text, Limit: 3, Now: s.Now}); len(hits) > 0 && hits[0].Score >= 0.6 && hits[0].Memory.OriginEvent != ev.ID {
					explained = true
				}
			}
			if explained {
				continue
			}
			q := "Why did this happen: " + truncate(text, 120) + "?"
			out = append(out, curiosityCandidate(ev, q, "unexplained:"+subjectKey(text), 0.5, drive))
		}
	}
	return out, nil
}

func curiosityCandidate(ev events.Event, question, subject string, weight, drive float64) brain.Candidate {
	return brain.Candidate{Kind: "question", Subject: subject, Summary: "question: " + question,
		Content: map[string]any{"question": question, "subject": subject, "source_event": string(ev.ID)},
		// A failed prediction or a contradiction is an unresolved conflict between what
		// was expected and what was seen, so the contradiction feature is set for both.
		Features: attention.Features{Salience: 0.4 + 0.45*weight, Novelty: 0.6 + 0.3*drive, Consequence: 0.5 + 0.3*weight, Urgency: 0.4 * weight, Confidence: 0.5,
			Contradiction: boolf(ev.Type == events.ContradictionDetected || ev.Type == events.PredictionError)},
		Confidence: 0.5, SourceEvents: []ids.ID{ev.ID}, Evidence: []events.EvidenceRef{{Kind: "event", ID: string(ev.ID), Note: "prompted the question"}}}
}

func hasLive(bs []beliefs.Belief) bool {
	for _, b := range bs {
		if b.Status != beliefs.StatusRejected && b.Status != beliefs.StatusSuperseded {
			return true
		}
	}
	return false
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func fmtID(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// subjectKey reduces text to its three most distinctive tokens so the same
// unexplained situation yields the same question subject.
func subjectKey(text string) string {
	toks := tokenise(text)
	var keep []string
	for _, t := range toks {
		if len(t) >= 4 && len(keep) < 3 {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "+")
}

func tokenise(text string) []string { return tokeniseWS(text) }
