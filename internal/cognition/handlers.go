package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Handlers are the executive actions for admitted thought kinds.
type Handlers struct {
	Memory  *memory.Store
	Beliefs *beliefs.Store
}

// Register installs the handlers on a registry.
func (h Handlers) Register(r *brain.Registry) {
	r.Handle("attend", h.attend)
	r.Handle("retrieve", h.retrieve)
	r.Handle("hypothesis", h.hypothesis)
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
		if e, err := c.Runtime.Events().Get(ctx, ids.ID(ref.ID)); err == nil {
			var p events.ObservationPayload
			if json.Unmarshal(e.Payload, &p) == nil && p.Trust.Valid() {
				cred = p.Trust.Credibility()
			}
		}
		ev = append(ev, beliefs.EvidenceInput{Direction: "for", Kind: "event", RefID: ref.ID, Source: "co-occurrence:" + ref.ID, Weight: 0.15, Credibility: cred, Note: "temporal co-occurrence (" + ref.Note + ")"})
	}
	res, err := h.Beliefs.Create(ctx, c.Tx, emitter(c), beliefs.New{MindID: c.State.Mind.ID, Proposition: prop, Epistemic: concepts.Hypothesis, Source: "brain.associate",
		Evidence: ev, Alternatives: []string{"coincidence: the two events are unrelated", "a third factor caused both"}, OriginEvent: t.EventID, Causation: t.EventID, Correlation: c.State.CycleID,
		Detail: map[string]any{"cause_event": t.Content["cause_event"], "effect_event": t.Content["effect_event"], "gap_seconds": t.Content["gap_seconds"]}})
	if err != nil {
		if errors.Is(err, beliefs.ErrBlocked) {
			c.Emit(events.New{Type: events.Metacognition, Source: "brain.executive", Summary: "hypothesis blocked by operator correction: " + prop, Salience: 0.3, Confidence: 1, Metadata: map[string]any{"check": "do_not_infer"}})
			return nil
		}
		return err
	}
	c.Runtime.Hold(workspace.Item{Key: "belief:" + string(res.Belief.ID), Content: "hypothesis (" + fmt.Sprintf("%.2f", res.Belief.Confidence) + "): " + prop, Kind: "thought", Ref: string(res.Belief.ID), SourceEvent: t.EventID})
	return nil
}

// attend encodes the attended event as a memory and holds it in working
// memory. Only attended events become episodes: attention gates encoding.
func (h Handlers) attend(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	evID, _ := t.Content["source_event"].(string)
	var mem memory.Memory
	if h.Memory != nil && evID != "" {
		ev, err := c.Runtime.Events().Get(ctx, ids.ID(evID))
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
