package cognition

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// SelfModelSync keeps each mind's self-model capability list equal to the
// effective policy, so the mind cannot claim tools it does not have.
type SelfModelSync struct {
	Minds  *minds.Store
	Caps   *capabilities.Service
	Events *evstore.Store
	Log    *slog.Logger
}

// Sync updates one mind (or all when mind is Nil) and emits
// self_model_updated only when something changed.
func (s SelfModelSync) Sync(ctx context.Context, mind ids.ID) {
	var list []minds.Mind
	if mind.IsZero() {
		list, _ = s.Minds.List(ctx, false)
	} else if m, err := s.Minds.Get(ctx, mind); err == nil {
		list = []minds.Mind{m}
	}
	for _, m := range list {
		inv, err := s.Caps.Inventory(ctx, m.ID)
		if err != nil {
			continue
		}
		sm, err := s.Minds.GetSelfModel(ctx, m.ID)
		if err != nil {
			continue
		}
		var caps []minds.CapabilityEntry
		for _, i := range inv {
			eff := string(i.Effect)
			if i.Capability.Locked {
				eff = "unavailable"
			}
			caps = append(caps, minds.CapabilityEntry{Name: i.Capability.Name, Description: i.Capability.Description, Policy: eff})
		}
		if fmt.Sprint(caps) == fmt.Sprint(sm.Capabilities) {
			continue
		}
		sm.Capabilities = caps
		sm.Refresh(m.Personality)
		if err := s.Minds.SaveSelfModel(ctx, m.ID, sm); err != nil {
			s.Log.Warn("self-model save failed", "mind", m.Slug, "err", err)
			continue
		}
		allow, ask := 0, 0
		for _, c := range caps {
			switch c.Policy {
			case "allow":
				allow++
			case "ask":
				ask++
			}
		}
		s.Events.Append(ctx, events.New{MindID: m.ID, Type: events.SelfModelUpdated, Source: "capabilities", Summary: fmt.Sprintf("self-model: %d capabilities allowed, %d need approval, rest denied", allow, ask), //nolint:errcheck
			Salience: 0.3, Confidence: 1, Metadata: map[string]any{"allow": allow, "ask": ask}, Payload: map[string]any{"capabilities": caps}})
	}
}
