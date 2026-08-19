package cognition

import (
	"context"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// linkEntities upserts the entities mentioned in text, links them to the
// given objects and records co-mention edges. Deterministic: the graph grows
// from what was actually said, and every edge cites the event it came from.
func (h Handlers) linkEntities(ctx context.Context, c *brain.Cycle, names []string, actor string, sourceEvent ids.ID, refs map[string]ids.ID) []graph.Entity {
	if h.Graph == nil {
		return nil
	}
	mind := c.State.Mind.ID
	var ents []graph.Entity
	seen := map[ids.ID]bool{}
	add := func(kind graph.Kind, name string) {
		e, err := h.Graph.Upsert(ctx, c.Tx, mind, kind, name, c.State.Now)
		if err != nil || seen[e.ID] {
			return
		}
		seen[e.ID] = true
		ents = append(ents, e)
		if !sourceEvent.IsZero() {
			h.Graph.LinkRef(ctx, c.Tx, mind, e.ID, "event", sourceEvent) //nolint:errcheck
		}
		for kind, id := range refs {
			if !id.IsZero() {
				h.Graph.LinkRef(ctx, c.Tx, mind, e.ID, kind, id) //nolint:errcheck
			}
		}
	}
	if actor != "" && actor != "operator" && actor != "system" {
		add(graph.Person, actor)
	}
	for _, n := range names {
		if len(n) >= 2 && len(ents) < 12 {
			add("", n)
		}
	}
	// The speaker discussed each entity; entities mentioned together merely
	// co-occur. Co-occurrence is association, never a causal or structural
	// claim; stronger relations need an explicit statement or an operator.
	for i := range ents {
		for j := i + 1; j < len(ents); j++ {
			rel := concepts.CoOccursWith
			if ents[i].Kind == graph.Person && i == 0 && actor != "" {
				rel = concepts.DiscussedWith
			}
			h.Graph.Relate(ctx, c.Tx, mind, ents[i].ID, ents[j].ID, rel, 0.3, "co-mention", sourceEvent) //nolint:errcheck
		}
	}
	return ents
}

// entitiesFromText runs the deterministic entity extractor over free text.
func entitiesFromText(text string) []string {
	return conversation.Analyse(text).Entities
}

var _ = events.Observation
