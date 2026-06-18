package brain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/pkg/ids"
)

// Manager owns one Runtime per active mind, wakes them on new events and
// reconciles with the minds table so enabling or disabling a mind in the UI
// takes effect without a restart.
type Manager struct {
	cfg      Config
	group    *scheduler.Group
	mu       sync.RWMutex
	runtimes map[ids.ID]*entry
}

type entry struct {
	rt     *Runtime
	cancel context.CancelFunc
	done   chan struct{}
}

// ErrNotRunning is returned for minds without a live runtime.
var ErrNotRunning = errors.New("mind runtime is not running")

// NewManager creates a manager; call Start to launch runtimes.
func NewManager(cfg Config, group *scheduler.Group) *Manager {
	return &Manager{cfg: cfg, group: group, runtimes: map[ids.ID]*entry{}}
}

// Start launches runtimes for active minds, subscribes to the event bus for
// wake-ups and reconciles every 30s.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.Reconcile(ctx); err != nil {
		return err
	}
	ch, err := m.cfg.Events.Bus().Subscribe(m.group.Context(), ids.Nil, 1024)
	if err != nil {
		return err
	}
	m.group.Go("brain.wake", func(ctx context.Context) error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case ev := <-ch:
				if strings.HasPrefix(ev.Source, "brain") {
					continue // our own output never re-triggers a cycle (T20)
				}
				if rt := m.Get(ev.MindID); rt != nil {
					rt.Wake()
				}
			}
		}
	})
	m.group.Every("brain.reconcile", 30*time.Second, m.Reconcile)
	return nil
}

// Reconcile starts runtimes for active minds and stops the rest.
func (m *Manager) Reconcile(ctx context.Context) error {
	list, err := m.cfg.Minds.List(ctx, true)
	if err != nil {
		return err
	}
	want := map[ids.ID]minds.Mind{}
	for _, md := range list {
		if md.Status == minds.StatusActive {
			want[md.ID] = md
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.runtimes {
		if _, ok := want[id]; !ok {
			e.cancel()
			<-e.done
			delete(m.runtimes, id)
			m.cfg.Log.Info("mind runtime stopped", "mind", id)
		}
	}
	for id, md := range want {
		if _, ok := m.runtimes[id]; ok {
			continue
		}
		rt, err := NewRuntime(ctx, m.cfg, md)
		if err != nil {
			m.cfg.Log.Error("cannot start mind runtime", "mind", md.Slug, "err", err)
			continue
		}
		rctx, cancel := context.WithCancel(m.group.Context())
		e := &entry{rt: rt, cancel: cancel, done: make(chan struct{})}
		m.runtimes[id] = e
		m.group.Go("brain."+md.Slug, func(context.Context) error {
			defer close(e.done)
			return rt.Run(rctx)
		})
	}
	return nil
}

// Get returns the runtime for a mind or nil.
func (m *Manager) Get(id ids.ID) *Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.runtimes[id]; ok {
		return e.rt
	}
	return nil
}

// Command forwards an operator command to a mind's runtime.
func (m *Manager) Command(ctx context.Context, id ids.ID, kind string, args map[string]bool) error {
	rt := m.Get(id)
	if rt == nil {
		return ErrNotRunning
	}
	return rt.Command(ctx, kind, args)
}

// Running lists the ids of minds with live runtimes.
func (m *Manager) Running() []ids.ID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ids.ID, 0, len(m.runtimes))
	for id := range m.runtimes {
		out = append(out, id)
	}
	return out
}
