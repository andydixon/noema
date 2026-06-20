package cognition

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Handlers are the executive actions for admitted thought kinds.
type Handlers struct {
	Memory *memory.Store
}

// Register installs the handlers on a registry.
func (h Handlers) Register(r *brain.Registry) {
	r.Handle("attend", h.attend)
	r.Handle("retrieve", h.retrieve)
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
