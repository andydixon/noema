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
		var grants []minds.GrantEntry
		if live, err := s.Caps.ScopedGrants(ctx, m.ID, true); err == nil {
			for _, g := range live {
				e := minds.GrantEntry{Capability: g.Capability, Target: g.TargetPattern, Access: string(g.Access), ExpiresAt: g.ExpiresAt}
				if g.MaxOperations != nil {
					left := *g.MaxOperations - g.UsedOperations
					e.Remaining = &left
				}
				grants = append(grants, e)
			}
		}
		if fmt.Sprint(caps) == fmt.Sprint(sm.Capabilities) && fmt.Sprint(grants) == fmt.Sprint(sm.Grants) {
			continue
		}
		sm.Capabilities, sm.Grants = caps, grants
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
		s.Events.Append(ctx, events.New{MindID: m.ID, Type: events.SelfModelUpdated, Source: "capabilities", Summary: fmt.Sprintf("self-model: %d capabilities allowed, %d need approval, %d temporary grants, rest denied", allow, ask, len(grants)), //nolint:errcheck
			Salience: 0.3, Confidence: 1, Metadata: map[string]any{"allow": allow, "ask": ask, "grants": len(grants)}, Payload: map[string]any{"capabilities": caps, "grants": grants}})
	}
}
