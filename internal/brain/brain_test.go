package brain

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/attention"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// echo proposes one candidate per new external event; repeat proposes the
// same subject every cycle to exercise the loop detector; boom panics.
type echo struct{ calls atomic.Int32 }

func (e *echo) Name() string { return "echo" }
func (e *echo) Observe(_ context.Context, s *State) ([]Candidate, error) {
	e.calls.Add(1)
	var out []Candidate
	for _, ev := range s.NewEvents {
		if ev.Type == events.Observation {
			out = append(out, Candidate{Kind: "attend", Subject: string(ev.ID), Summary: "noticed: " + ev.Summary, Features: attention.Features{Salience: ev.Salience, Urgency: 0.5}, Confidence: 1, SourceEvents: []ids.ID{ev.ID}})
		}
	}
	return out, nil
}

type repeat struct{}

func (repeat) Name() string { return "repeat" }
func (repeat) Observe(_ context.Context, s *State) ([]Candidate, error) {
	return []Candidate{{Kind: "note", Subject: "same-thing", Summary: "the same thought again", Features: attention.Features{Salience: 0.9}, Confidence: 0.5}}, nil
}

type boom struct{}

func (boom) Name() string                                         { return "boom" }
func (boom) Observe(context.Context, *State) ([]Candidate, error) { panic("malformed input") }

type failing struct{}

func (failing) Name() string { return "failing" }
func (failing) Observe(context.Context, *State) ([]Candidate, error) {
	return nil, errors.New("upstream down")
}

func setup(t *testing.T, reg *Registry) (Config, minds.Mind, *evstore.Store) {
	t.Helper()
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	m, err := ms.Create(ctx, minds.CreateParams{Slug: "brain"})
	if err != nil {
		t.Fatal(err)
	}
	es := evstore.NewStore(pool, evstore.NewBus(16), nil)
	cfg := Config{DB: pool, Events: es, Minds: ms, Registry: reg, Pool: scheduler.NewPool(2), Log: slog.New(slog.NewTextHandler(os.Stderr, nil)), Services: Services{Events: es}}
	return cfg, m, es
}

func TestCycleRecordsProvenanceAndPersists(t *testing.T) {
	e := &echo{}
	cfg, m, es := setup(t, NewRegistry().AddProcess(e).AddProcess(boom{}).AddProcess(failing{}))
	ctx := context.Background()
	rt, err := NewRuntime(ctx, cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	rt.now = func() time.Time { return time.Now().Add(time.Second) } // events are "settled"
	obs, _ := es.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "test", Summary: "load spike on atlas", Salience: 0.9, Confidence: 1})
	rt.runCycle(ctx, "operator")

	if e.calls.Load() != 1 {
		t.Fatalf("echo called %d times", e.calls.Load())
	}
	st := rt.copyState()
	if st.Cycle != 1 || len(st.Workspace) != 1 || st.Workspace[0].Subject != string(obs[0].ID) || len(st.WorkingMemory) != 1 {
		t.Fatalf("workspace wrong: %+v", st)
	}
	all, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Ascending: true})
	types := map[events.Type]int{}
	var selected events.Event
	for _, ev := range all {
		types[ev.Type]++
		if ev.Type == events.ThoughtSelected {
			selected = ev
		}
	}
	for _, want := range []events.Type{events.ThoughtCandidate, events.ThoughtSelected, events.WorkspaceBroadcast, events.AttentionShift, events.CycleCompleted} {
		if types[want] == 0 {
			t.Fatalf("missing %s in %v", want, types)
		}
	}
	// Provenance: selected thought → candidate event → observation.
	if selected.CausationID.IsZero() {
		t.Fatal("thought_selected has no causation")
	}
	cand, _ := es.Get(ctx, selected.CausationID)
	if cand.Type != events.ThoughtCandidate || cand.CausationID != obs[0].ID {
		t.Fatalf("provenance chain broken: %+v", cand)
	}
	if cur, _ := es.Cursor(ctx, cfg.DB, m.ID, cursorConsumer); cur != obs[0].Seq {
		t.Fatalf("cursor %d, want %d", cur, obs[0].Seq)
	}
	// Restart continuity: a fresh runtime loads the same state.
	rt2, err := NewRuntime(ctx, cfg, m)
	if err != nil || rt2.state.Cycle != 1 || len(rt2.state.Workspace) != 1 {
		t.Fatalf("state not persisted: %v %+v", err, rt2.state)
	}
	// A second cycle with nothing new triggered by stimulus does nothing.
	rt2.now = rt.now
	rt2.runCycle(ctx, "stimulus")
	if rt2.state.Cycle != 1 {
		t.Fatal("cycle ran without input")
	}
}

func TestLoopDetectorSuppressesRepeats(t *testing.T) {
	cfg, m, es := setup(t, NewRegistry().AddProcess(repeat{}))
	ctx := context.Background()
	m.Config.LoopDetectThreshold, m.Config.LoopDetectWindow = 3, 10
	rt, err := NewRuntime(ctx, cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	rt.now = func() time.Time { return time.Now().Add(time.Second) }
	for i := 0; i < 6; i++ {
		es.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "test", Summary: "tick", Salience: 0.1, Confidence: 1}) //nolint:errcheck
		rt.runCycle(ctx, "operator")
	}
	meta, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{events.Metacognition}})
	if len(meta) != 1 {
		t.Fatalf("expected exactly one loop warning, got %d", len(meta))
	}
	cands, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{events.ThoughtCandidate}})
	var suppressed int
	for _, c := range cands {
		if c.Metadata["fate"] == "suppressed" {
			suppressed++
		}
	}
	if suppressed != 3 {
		t.Fatalf("expected 3 suppressed candidates, got %d", suppressed)
	}
}

func TestPauseResetAndCancellation(t *testing.T) {
	cfg, m, es := setup(t, NewRegistry().AddProcess(&echo{}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, _ := NewRuntime(ctx, cfg, m)
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }() //nolint:errcheck
	if err := rt.Command(ctx, "pause", nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.Command(ctx, "cycle", nil); err == nil {
		t.Fatal("cycle should be refused while paused")
	}
	if err := rt.Command(ctx, "resume", nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.Command(ctx, "reset", map[string]bool{"workspace": true, "working_memory": true}); err != nil {
		t.Fatal(err)
	}
	modes, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{events.ModeChanged}})
	if len(modes) != 2 {
		t.Fatalf("expected 2 mode changes, got %d", len(modes))
	}
	st, err := rt.Snapshot(ctx)
	if err != nil || st.Mode != string(ModeAwake) {
		t.Fatalf("snapshot: %v %+v", err, st)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop on cancellation")
	}
	_ = workspace.State{}
}
