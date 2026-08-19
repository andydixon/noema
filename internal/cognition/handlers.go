package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/epistemics"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Handlers are the executive actions for admitted thought kinds.
type Handlers struct {
	Memory        *memory.Store
	Beliefs       *beliefs.Store
	Conversations *conversation.Store
	Graph         *graph.Store
	Assertions    *epistemics.Store // nil disables assertion records (claims still become evidence)
}

// Register installs the handlers on a registry.
func (h Handlers) Register(r *brain.Registry) {
	r.Handle("attend", h.attend)
	r.Handle("retrieve", h.retrieve)
	r.Handle("hypothesis", h.hypothesis)
	r.Handle("perceive", h.perceive)
	r.Handle("goal", h.goal)
	r.Handle("question", h.question)
	r.Handle("metacognition", h.metacognition)
	r.Handle("action", h.action)
}

// emitter adapts the cycle to the beliefs store's Emitter.
func emitter(c *brain.Cycle) beliefs.Emitter {
	return func(ctx context.Context, _ storage.Querier, n events.New) (events.Event, error) {
		return c.EmitNow(ctx, n)
	}
}

// hypothesis turns an admitted hypothesis candidate into a belief whose
// confidence is computed from the cited observations. Temporal co-occurrence
// is weak evidence, so weights are small and the result stays a hypothesis
// until independent support arrives.
func (h Handlers) hypothesis(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	if h.Beliefs == nil {
		return nil
	}
	prop, _ := t.Content["proposition"].(string)
	if prop == "" {
		prop = strings.TrimPrefix(t.Summary, "hypothesis: ")
	}
	var ev []beliefs.EvidenceInput
	for _, ref := range t.Evidence {
		if ref.Kind != "event" {
			continue
		}
		cred := 0.6
		family := "event:" + ref.ID
		if e, err := c.Runtime.Events().Get(ctx, ids.ID(ref.ID)); err == nil {
			var p events.ObservationPayload
			if json.Unmarshal(e.Payload, &p) == nil && p.Trust.Valid() {
				cred = p.Trust.Credibility()
			}
			if f := beliefs.FamilyFromEvent(e); f != "" {
				family = f
			}
		}
		// The two observations are the roots; the causal reading is an inference over them.
		ev = append(ev, beliefs.EvidenceInput{Direction: "for", Kind: "event", RefID: ref.ID, Source: "co-occurrence:" + ref.ID, Weight: 0.15, Credibility: cred, Note: "temporal co-occurrence (" + ref.Note + ")",
			Derivation: beliefs.DerivationInference, Family: family, RootIDs: []string{ref.ID}})
	}
	res, err := h.Beliefs.Create(ctx, c.Tx, emitter(c), beliefs.New{MindID: c.State.Mind.ID, Proposition: prop, Epistemic: concepts.Hypothesis, Source: "brain.associate",
		Evidence: ev, Alternatives: []string{"coincidence: the two events are unrelated", "a third factor caused both"}, OriginEvent: t.EventID, Causation: t.EventID, Correlation: c.State.CycleID,
		Detail: map[string]any{"cause_event": t.Content["cause_event"], "effect_event": t.Content["effect_event"], "gap_seconds": t.Content["gap_seconds"], "relation": string(concepts.PossiblyCausedBy), "basis": "temporal association only"}})
	if err != nil {
		if errors.Is(err, beliefs.ErrBlocked) {
			c.Emit(events.New{Type: events.Metacognition, Source: "brain.executive", Summary: "hypothesis blocked by operator correction: " + prop, Salience: 0.3, Confidence: 1, Metadata: map[string]any{"check": "do_not_infer"}})
			return nil
		}
		return err
	}
	c.Runtime.Hold(workspace.Item{Key: "belief:" + string(res.Belief.ID), Content: "hypothesis (" + fmt.Sprintf("%.2f", res.Belief.Confidence) + "): " + prop, Kind: "thought", Ref: string(res.Belief.ID), SourceEvent: t.EventID})
	// A causal hypothesis implies a checkable expectation: the effect recurs
	// after the cause. Its confidence is the belief's, so a miss will lower it.
	if c.State.Services.Predictions != nil {
		effect, _ := t.Content["effect_event"].(string)
		if ev, err := c.Runtime.Events().Get(ctx, ids.ID(effect)); err == nil {
			terms := distinctiveTerms(ev.Summary, 3)
			if len(terms) > 0 {
				p, err := c.State.Services.Predictions.Create(ctx, c.Tx, predictions.New{MindID: c.State.Mind.ID, Statement: "something like \"" + truncate(ev.Summary, 80) + "\" will recur within 24 hours if the cause is real",
					Subject: subjectKey(ev.Summary), Confidence: res.Belief.Confidence, WindowEnd: c.State.Now.Add(24 * time.Hour),
					Criteria: predictions.Criteria{Kind: "event_match", Expect: "occurs", Terms: terms, MinTerms: min(2, len(terms))}, Source: "brain.associate", OriginEvent: res.Events[0].ID, BeliefID: res.Belief.ID})
				if err == nil {
					c.Emit(events.New{Type: events.PredictionCreated, Source: "brain.associate", Summary: fmt.Sprintf("predicted (%.2f): %s", p.Confidence, p.Statement), Salience: 0.3, Confidence: p.Confidence, CausationID: res.Events[0].ID,
						Evidence: []events.EvidenceRef{{Kind: "prediction", ID: string(p.ID)}, {Kind: "belief", ID: string(res.Belief.ID)}}, Metadata: map[string]any{"prediction_id": string(p.ID), "belief_id": string(res.Belief.ID), "window_end": p.WindowEnd}, Payload: p})
				}
			}
		}
	}
	return nil
}

// distinctiveTerms picks the longest tokens of a summary as match terms.
func distinctiveTerms(text string, n int) []string {
	toks := tokeniseWS(text)
	var out []string
	for _, t := range toks {
		if len(t) >= 5 {
			out = append(out, t)
		}
		if len(out) == n {
			break
		}
	}
	return out
}

// attend encodes the attended event as a memory and holds it in working
// memory. Only attended events become episodes: attention gates encoding.
func (h Handlers) attend(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	evID, _ := t.Content["source_event"].(string)
	var mem memory.Memory
	if h.Memory != nil && evID != "" {
		ev, err := c.Runtime.Events().Get(ctx, ids.ID(evID))
		if err == nil && ev.Metadata["conversation_id"] != nil {
			err = errSkipEncode // the perceive handler encodes conversational episodes
		}
		if err == nil {
			n := memory.New{MindID: c.State.Mind.ID, Kind: concepts.Episodic, Epistemic: concepts.Observation, Content: ev.Summary, Source: ev.Source,
				Trust: events.TrustLow, Confidence: ev.Confidence, Importance: t.Score, Salience: ev.Salience, Valence: ev.Valence, Arousal: ev.Arousal, OccurredAt: ev.OccurredAt, OriginEvent: ev.ID,
				Detail: map[string]any{"event_type": ev.Type, "participants": []string{}}}
			switch ev.Type {
			case events.Observation:
				var p events.ObservationPayload
				if json.Unmarshal(ev.Payload, &p) == nil {
					if p.Text != "" {
						n.Content = p.Text
					}
					n.Trust = p.Trust
					n.Detail["channel"], n.Detail["reference"], n.Detail["method"] = p.Channel, p.Reference, p.Method
					if p.Actor != "" {
						n.Detail["participants"] = []string{p.Actor}
					}
				}
			case events.OperatorAction:
				n.Kind, n.Trust, n.Importance = concepts.Autobiographical, events.TrustOperator, max(t.Score, 0.6)
				n.Content = "An operator changed me: " + ev.Summary
			default:
				n.Trust = events.TrustSystem
			}
			mem, err = h.Memory.Create(ctx, c.Tx, n)
			if err != nil {
				return fmt.Errorf("encode memory: %w", err)
			}
			c.Emit(events.New{Type: events.MemoryCreated, Source: "brain.memory", Summary: fmt.Sprintf("encoded %s memory: %s", mem.Kind, truncate(mem.Content, 120)),
				Salience: 0.3, Confidence: mem.Confidence, CausationID: t.EventID, ParentIDs: []ids.ID{t.EventID, ev.ID},
				Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(mem.ID)}, {Kind: "event", ID: string(ev.ID), Note: "source"}},
				Metadata: map[string]any{"memory_id": mem.ID, "kind": mem.Kind}, Payload: map[string]any{"memory_id": mem.ID, "kind": mem.Kind, "importance": mem.Importance, "trust": mem.Trust}})
		}
	}
	item := workspace.Item{Key: "attend:" + t.Subject, Content: t.Summary, Kind: "observation", SourceEvent: ids.ID(evID)}
	if !mem.ID.IsZero() {
		item.Ref = string(mem.ID)
		text, _ := t.Content["text"].(string)
		h.linkEntities(ctx, c, entitiesFromText(text), "", ids.ID(evID), map[string]ids.ID{"memory": mem.ID})
	}
	c.Runtime.Hold(item)
	return nil
}

// retrieve reinforces a recalled memory, records the retrieval with its
// scoring components, and holds it in working memory.
func (h Handlers) retrieve(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	ref, _ := t.Content["ref"].(string)
	if h.Memory == nil || ref == "" {
		return nil
	}
	mid := ids.ID(ref)
	if err := h.Memory.Reinforce(ctx, c.Tx, c.State.Mind.ID, mid); err != nil {
		return err
	}
	src, _ := t.Content["source_event"].(string)
	c.Emit(events.New{Type: events.MemoryRetrieved, Source: "brain.memory", Summary: t.Summary, Salience: t.Score, Confidence: t.Confidence,
		CausationID: t.EventID, ParentIDs: []ids.ID{t.EventID}, Evidence: t.Evidence,
		Metadata: map[string]any{"memory_id": mid, "score": t.Content["score"]}, Payload: map[string]any{"memory_id": mid, "score": t.Content["score"], "components": t.Content["components"], "query": t.Content["query"]}})
	c.Runtime.Hold(workspace.Item{Key: "memory:" + ref, Content: t.Summary, Kind: "retrieval", Ref: ref, SourceEvent: ids.ID(src)})
	return nil
}

var errSkipEncode = errors.New("skip")
