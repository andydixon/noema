// Package agency holds end-to-end tests of volition, planning and acting
// through the assembled daemon (PLAN §8, Part J scenarios).
package agency

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/tests/harness"
)

// fakeHost stands in for systemd.
type fakeHost struct {
	mu           sync.Mutex
	active       map[string]bool
	fixOnRestart bool
	restarts     int
	journal      string
}

func newHost() *fakeHost { return &fakeHost{active: map[string]bool{}} }

func (f *fakeHost) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	unit := args[len(args)-1]
	switch {
	case name == "systemctl" && args[0] == "restart":
		f.restarts++
		if f.fixOnRestart {
			f.active[unit] = true
		}
		return nil, nil
	case name == "systemctl" && args[0] == "show":
		state := "failed"
		if f.active[unit] {
			state = "active"
		}
		return []byte("LoadState=loaded\nActiveState=" + state + "\nSubState=" + state + "\n"), nil
	case name == "journalctl":
		return []byte(f.journal), nil
	}
	return nil, fmt.Errorf("unexpected %s %v", name, args)
}

func (f *fakeHost) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.restarts }

// waitFor drives cycles and the executor until cond holds or the timeout passes.
func waitFor(t *testing.T, h *harness.Harness, m minds.Mind, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(350 * time.Millisecond)
		h.Deps.Brain.Command(context.Background(), m.ID, "cycle", nil) //nolint:errcheck
		h.Deps.Executor.TickMind(context.Background(), m.ID)           //nolint:errcheck
	}
	if !cond() {
		evs, _ := h.Deps.Events.Query(context.Background(), evstore.Filter{MindID: m.ID, Limit: 200, Ascending: true})
		for _, ev := range evs {
			t.Logf("%s %-22s %s", ev.Source, ev.Type, ev.Summary)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
}
