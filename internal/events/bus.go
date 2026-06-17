// Package events stores cognitive events append-only and fans them out to
// live subscribers. Durable consumers read by sequence cursor; the in-memory
// bus is only a wake-up and a feed for the UI.
package events

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Bus fans events out to subscribers with bounded buffers. A slow subscriber
// loses events (counted in Dropped) rather than slowing the publisher; SSE
// clients recover via Last-Event-ID and durable consumers via cursors.
type Bus struct {
	mu      sync.RWMutex
	subs    map[uint64]*subscriber
	nextID  uint64
	dropped atomic.Int64
	max     int
}

type subscriber struct {
	mind ids.ID // Nil = all minds
	ch   chan events.Event
}

// NewBus creates a bus allowing at most max subscribers (T18).
func NewBus(max int) *Bus {
	return &Bus{subs: map[uint64]*subscriber{}, max: max}
}

// ErrTooManySubscribers is returned when the subscriber cap is reached.
type ErrTooManySubscribers struct{}

func (ErrTooManySubscribers) Error() string { return "too many event subscribers" }

// Subscribe returns a channel of events for one mind (or all when mind is
// Nil). The subscription ends when ctx is cancelled.
func (b *Bus) Subscribe(ctx context.Context, mind ids.ID, buffer int) (<-chan events.Event, error) {
	if buffer < 1 {
		buffer = 64
	}
	b.mu.Lock()
	if len(b.subs) >= b.max {
		b.mu.Unlock()
		return nil, ErrTooManySubscribers{}
	}
	id := b.nextID
	b.nextID++
	s := &subscriber{mind: mind, ch: make(chan events.Event, buffer)}
	b.subs[id] = s
	b.mu.Unlock()
	context.AfterFunc(ctx, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	})
	return s.ch, nil
}

// Publish delivers events to matching subscribers without blocking.
func (b *Bus) Publish(evs ...events.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ev := range evs {
		for _, s := range b.subs {
			if !s.mind.IsZero() && s.mind != ev.MindID {
				continue
			}
			select {
			case s.ch <- ev:
			default:
				b.dropped.Add(1)
			}
		}
	}
}

// Subscribers returns the current subscriber count.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Dropped returns the number of events dropped for slow subscribers.
func (b *Bus) Dropped() int64 { return b.dropped.Load() }
