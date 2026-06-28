package cognition

import (
	"context"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/notifications"
	"github.com/andydixon/noema/pkg/events"
)

// NotifyHook runs the notification rules over the cycle's events and records
// what fired or was suppressed, with the reason either way.
type NotifyHook struct {
	Store *notifications.Store
}

// Name implements brain.Hook.
func (NotifyHook) Name() string { return "notify" }

// AfterCycle implements brain.Hook.
func (h NotifyHook) AfterCycle(ctx context.Context, c *brain.Cycle, emitted []events.Event) error {
	if h.Store == nil {
		return nil
	}
	m := c.State.Mind
	all := append(append([]events.Event{}, c.State.NewEvents...), emitted...)
	for _, ev := range all {
		if ev.Type == events.NotificationCreated || ev.Type == events.NotificationSuppressed {
			continue
		}
		for _, cand := range notifications.Rules(ev, m.Config, m.Personality) {
			n, suppressed, err := h.Store.Deliver(ctx, c.Tx, m.ID, cand, m.Config.NotifyCooldown.D())
			if err != nil {
				return err
			}
			typ, summary := events.NotificationCreated, "notified ("+cand.Level+"): "+cand.Title
			if suppressed {
				typ, summary = events.NotificationSuppressed, "notification aggregated (repeat within cooldown): "+cand.Title
			}
			c.Emit(events.New{Type: typ, Source: "brain.notify", Summary: summary, Salience: 0.2, Confidence: 1, CausationID: ev.ID,
				Evidence: []events.EvidenceRef{{Kind: "event", ID: string(ev.ID), Note: "trigger"}},
				Metadata: map[string]any{"notification_id": string(n.ID), "rule": cand.Rule, "level": cand.Level, "suppressed": suppressed, "count": n.Count},
				Payload:  map[string]any{"rule": cand.Rule, "reason": cand.Reason, "title": cand.Title, "aggregate_key": cand.AggregateKey, "count": n.Count}})
		}
	}
	return nil
}
