package events

import (
	"context"
	"sort"

	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Causal chains (review pass §7.2 item 11). A chain is a bounded walk over
// the provenance links events actually record: causation_id ("caused"),
// parent_ids ("parent") and evidence references to other events ("cited").
// Membership of the same cognitive cycle is reported on each node but is
// never an edge: it is correlation, and the interface must say so.

// Edge kinds.
const (
	EdgeCaused = "caused" // to.causation_id = from
	EdgeParent = "parent" // from ∈ to.parent_ids
	EdgeCited  = "cited"  // to.evidence references from
)

// Chain limits. Depth counts hops from the origin; nodes caps the whole walk.
const (
	MaxChainDepth = 6
	MaxChainNodes = 200
)

// ChainNode is one event in a chain with its distance from the origin.
type ChainNode struct {
	Event     events.Event         `json:"event"`
	Depth     int                  `json:"depth"`     // negative = upstream (cause side), positive = downstream
	CycleID   ids.ID               `json:"cycle_id"`  // correlation id; grouping only, not causation
	Artefacts []events.EvidenceRef `json:"artefacts"` // non-event references the node cites (beliefs, memories, calls)
}

// ChainEdge is one recorded relationship between two events in the chain.
type ChainEdge struct {
	From ids.ID `json:"from"`
	To   ids.ID `json:"to"`
	Kind string `json:"kind"`
}

// Chain is the result of a bounded walk.
type Chain struct {
	Origin    ids.ID      `json:"origin"`
	Direction string      `json:"direction"`
	Depth     int         `json:"depth"`
	Nodes     []ChainNode `json:"nodes"`
	Edges     []ChainEdge `json:"edges"`
	Truncated bool        `json:"truncated"` // the node cap stopped the walk
}

// Chain walks back (towards causes), forward (towards consequences) or both
// from an event, up to depth hops and MaxChainNodes nodes. Only recorded
// links are followed; nothing is inferred. Visited sets make cycles harmless.
func (s *Store) Chain(ctx context.Context, origin ids.ID, direction string, depth int) (Chain, error) {
	if depth <= 0 || depth > MaxChainDepth {
		depth = MaxChainDepth
	}
	if direction != "back" && direction != "forward" {
		direction = "both"
	}
	start, err := s.Get(ctx, origin)
	if err != nil {
		return Chain{}, err
	}
	c := Chain{Origin: origin, Direction: direction, Depth: depth}
	seen := map[ids.ID]bool{origin: true}
	edgeSeen := map[[3]string]bool{}
	nodes := map[ids.ID]ChainNode{origin: node(start, 0)}
	addEdge := func(from, to ids.ID, kind string) {
		k := [3]string{string(from), string(to), kind}
		if edgeSeen[k] {
			return
		}
		edgeSeen[k] = true
		c.Edges = append(c.Edges, ChainEdge{From: from, To: to, Kind: kind})
	}
	type item struct {
		ev events.Event
		d  int
	}
	// Upstream: what this event says it came from.
	if direction != "forward" {
		queue := []item{{start, 0}}
		for len(queue) > 0 && len(nodes) < MaxChainNodes {
			cur := queue[0]
			queue = queue[1:]
			if -cur.d >= depth {
				continue
			}
			var upstream []ids.ID
			kinds := map[ids.ID]string{}
			if !cur.ev.CausationID.IsZero() {
				upstream = append(upstream, cur.ev.CausationID)
				kinds[cur.ev.CausationID] = EdgeCaused
			}
			for _, p := range cur.ev.ParentIDs {
				if _, ok := kinds[p]; !ok {
					upstream = append(upstream, p)
					kinds[p] = EdgeParent
				}
			}
			for _, ref := range cur.ev.Evidence {
				if ref.Kind != "event" {
					continue
				}
				if id, err := ids.Parse(ref.ID); err == nil {
					if _, ok := kinds[id]; !ok {
						upstream = append(upstream, id)
						kinds[id] = EdgeCited
					}
				}
			}
			if len(upstream) == 0 {
				continue
			}
			evs, err := s.GetMany(ctx, upstream)
			if err != nil {
				return c, err
			}
			for _, e := range evs {
				addEdge(e.ID, cur.ev.ID, kinds[e.ID])
				if seen[e.ID] {
					continue
				}
				if len(nodes) >= MaxChainNodes {
					c.Truncated = true
					break
				}
				seen[e.ID] = true
				nodes[e.ID] = node(e, cur.d-1)
				queue = append(queue, item{e, cur.d - 1})
			}
		}
	}
	// Downstream: what later events say came from this one.
	if direction != "back" {
		queue := []item{{start, 0}}
		for len(queue) > 0 && len(nodes) < MaxChainNodes {
			cur := queue[0]
			queue = queue[1:]
			if cur.d >= depth {
				continue
			}
			children, err := s.Children(ctx, cur.ev.ID, 50)
			if err != nil {
				return c, err
			}
			cited, err := s.DerivedFrom(ctx, "event", string(cur.ev.ID), 50)
			if err != nil {
				return c, err
			}
			for _, e := range children {
				kind := EdgeParent
				if e.CausationID == cur.ev.ID {
					kind = EdgeCaused
				}
				addEdge(cur.ev.ID, e.ID, kind)
			}
			for _, e := range cited {
				addEdge(cur.ev.ID, e.ID, EdgeCited)
			}
			for _, e := range append(children, cited...) {
				if seen[e.ID] {
					continue
				}
				if len(nodes) >= MaxChainNodes {
					c.Truncated = true
					break
				}
				seen[e.ID] = true
				nodes[e.ID] = node(e, cur.d+1)
				queue = append(queue, item{e, cur.d + 1})
			}
		}
	}
	for _, n := range nodes {
		c.Nodes = append(c.Nodes, n)
	}
	sort.Slice(c.Nodes, func(i, j int) bool { return c.Nodes[i].Event.Seq < c.Nodes[j].Event.Seq })
	sort.Slice(c.Edges, func(i, j int) bool {
		if c.Edges[i].From != c.Edges[j].From {
			return c.Edges[i].From < c.Edges[j].From
		}
		return c.Edges[i].To < c.Edges[j].To
	})
	if c.Edges == nil {
		c.Edges = []ChainEdge{}
	}
	return c, nil
}

func node(e events.Event, depth int) ChainNode {
	n := ChainNode{Event: e, Depth: depth, CycleID: e.CorrelationID, Artefacts: []events.EvidenceRef{}}
	for _, ref := range e.Evidence {
		if ref.Kind != "event" {
			n.Artefacts = append(n.Artefacts, ref)
		}
	}
	return n
}
