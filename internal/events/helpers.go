package events

import (
	"context"
	"strings"

	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// OperatorAction records an administrative change as a cognitive event so the
// mind's history shows when and why it was altered from outside.
func (s *Store) OperatorAction(ctx context.Context, mind ids.ID, actor, action, summary string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["action"], details["actor"] = action, actor
	s.Append(ctx, events.New{MindID: mind, Type: events.OperatorAction, Source: "operator", Summary: summary, Salience: 0.6, Confidence: 1, //nolint:errcheck
		Metadata: map[string]any{"action": action, "actor": actor}, Payload: details})
}

// Matches applies the parts of a Filter that can be evaluated in memory, for
// live streams.
func (f Filter) Matches(ev events.Event) bool {
	if !f.MindID.IsZero() && ev.MindID != f.MindID {
		return false
	}
	if len(f.Types) > 0 && !containsType(f.Types, ev.Type) {
		return false
	}
	if len(f.Categories) > 0 && !containsCat(f.Categories, ev.Category) {
		return false
	}
	if len(f.Sources) > 0 && !containsStr(f.Sources, ev.Source) {
		return false
	}
	if ev.Salience < f.MinSalience || ev.Confidence < f.MinConfidence {
		return false
	}
	if f.MaxConfidence > 0 && ev.Confidence > f.MaxConfidence {
		return false
	}
	if !f.CorrelationID.IsZero() && ev.CorrelationID != f.CorrelationID {
		return false
	}
	if q := strings.TrimSpace(f.Search); q != "" && !strings.Contains(strings.ToLower(ev.Summary), strings.ToLower(q)) {
		return false
	}
	return true
}

func containsType(xs []events.Type, x events.Type) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func containsCat(xs []events.Category, x events.Category) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
