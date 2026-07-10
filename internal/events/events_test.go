package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestBusBoundedFanout(t *testing.T) {
	b := NewBus(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m1, m2 := ids.New(), ids.New()
	all, _ := b.Subscribe(ctx, ids.Nil, 1)
	only1, _ := b.Subscribe(ctx, m1, 8)
	if _, err := b.Subscribe(ctx, ids.Nil, 1); err == nil {
		t.Fatal("subscriber cap not enforced")
	}
	b.Publish(events.Event{MindID: m1, Type: events.Observation}, events.Event{MindID: m2, Type: events.Observation}, events.Event{MindID: m1, Type: events.Reflection})
	if len(only1) != 2 {
		t.Fatalf("mind filter wrong: %d", len(only1))
	}
	if len(all) != 1 || b.Dropped() != 2 {
		t.Fatalf("expected 1 buffered and 2 dropped, got %d / %d", len(all), b.Dropped())
	}
	cancel()
	time.Sleep(5 * time.Millisecond)
	if b.Subscribers() != 0 {
		t.Fatal("subscribers not removed on cancel")
	}
}

func TestStoreAppendQueryImmutableAndCursors(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, err := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "ev"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "other"})
	s := NewStore(pool, NewBus(10), nil)
	sub, _ := s.Bus().Subscribe(ctx, m.ID, 16)

	if _, err := s.Append(ctx, events.New{MindID: m.ID, Type: "bogus", Summary: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown type accepted: %v", err)
	}
	obs, err := s.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "test", Summary: "load spike observed", Salience: 0.9, Confidence: 1.2, Payload: map[string]any{"cpu": 97}})
	if err != nil {
		t.Fatal(err)
	}
	if obs[0].Confidence != 1 || obs[0].Seq == 0 || obs[0].Category != events.CatPerception {
		t.Fatalf("normalisation or seq wrong: %+v", obs[0])
	}
	hyp, _ := s.Append(ctx, events.New{MindID: m.ID, Type: events.HypothesisCreated, Source: "reasoning", Summary: "disk contention hypothesis",
		CausationID: obs[0].ID, ParentIDs: []ids.ID{obs[0].ID}, CorrelationID: obs[0].ID, Confidence: 0.4, Salience: 0.7,
		Evidence: []events.EvidenceRef{{Kind: "event", ID: string(obs[0].ID)}}})
	s.Append(ctx, events.New{MindID: other.ID, Type: events.Observation, Source: "test", Summary: "other mind sees something else"}) //nolint:errcheck

	// Live delivery.
	if len(sub) != 2 {
		t.Fatalf("expected 2 live events for mind, got %d", len(sub))
	}
	// Mind scoping and filters.
	got, _ := s.Query(ctx, Filter{MindID: m.ID})
	if len(got) != 2 || got[0].Type != events.HypothesisCreated {
		t.Fatalf("query wrong: %+v", got)
	}
	got, _ = s.Query(ctx, Filter{MindID: m.ID, Types: []events.Type{events.Observation}, MinSalience: 0.5})
	if len(got) != 1 {
		t.Fatalf("type filter wrong: %d", len(got))
	}
	got, _ = s.Query(ctx, Filter{MindID: m.ID, Search: "contention"})
	if len(got) != 1 {
		t.Fatalf("search wrong: %d", len(got))
	}
	got, _ = s.Query(ctx, Filter{MindID: m.ID, AfterSeq: obs[0].Seq, Ascending: true})
	if len(got) != 1 || got[0].ID != hyp[0].ID {
		t.Fatalf("cursor paging wrong: %+v", got)
	}
	// Causal navigation.
	kids, _ := s.Children(ctx, obs[0].ID, 10)
	if len(kids) != 1 || kids[0].ID != hyp[0].ID {
		t.Fatalf("children wrong: %+v", kids)
	}
	derived, _ := s.DerivedFrom(ctx, "event", string(obs[0].ID), 10)
	if len(derived) != 1 {
		t.Fatalf("evidence lookup wrong: %d", len(derived))
	}
	// Immutability.
	if _, err := pool.Exec(ctx, `UPDATE cognitive_events SET summary='rewritten' WHERE id=$1`, obs[0].ID); err == nil {
		t.Fatal("events must be append-only")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM cognitive_events`); err == nil {
		t.Fatal("events must not be deletable")
	}
	// Cursors never move backwards.
	s.SetCursor(ctx, pool, m.ID, "brain", hyp[0].Seq) //nolint:errcheck
	s.SetCursor(ctx, pool, m.ID, "brain", obs[0].Seq) //nolint:errcheck
	cur, _ := s.Cursor(ctx, pool, m.ID, "brain")
	if cur != hyp[0].Seq {
		t.Fatalf("cursor regressed: %d", cur)
	}
	st, _ := s.Stats(ctx, m.ID, time.Time{})
	if st.Total != 2 || st.ByCategory["belief"] != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

// TestCursorsToleratePlaceholderGaps burns sequence values the way a rolled
// back insert does and checks that cursors, forward paging and SSE-style
// resumption (seq > last) see every real event and nothing else. A gap in
// seq must never be mistaken for a missing event (ADR-0002).
func TestCursorsToleratePlaceholderGaps(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, err := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "gaps"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool, NewBus(10), nil)
	first, err := s.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "test", Summary: "one"})
	if err != nil {
		t.Fatal(err)
	}
	// A failed insert consumes a sequence value and rolls back; emulate several.
	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(ctx, `SELECT nextval(pg_get_serial_sequence('cognitive_events','seq'))`); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ := pool.Begin(ctx)
	if _, err := s.AppendIn(ctx, tx, events.New{MindID: m.ID, Type: events.Observation, Source: "test", Summary: "rolled back"}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx) //nolint:errcheck
	second, err := s.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "test", Summary: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Seq <= first[0].Seq+1 {
		t.Fatalf("test did not create a gap: %d then %d", first[0].Seq, second[0].Seq)
	}
	// Forward paging from the cursor sees exactly the real events after it.
	after, err := s.Query(ctx, Filter{MindID: m.ID, AfterSeq: first[0].Seq, Ascending: true, Limit: 10})
	if err != nil || len(after) != 1 || after[0].ID != second[0].ID {
		t.Fatalf("paging across a gap: %v %+v", err, after)
	}
	// A cursor parked inside the gap still resumes at the next real event.
	if err := s.SetCursor(ctx, pool, m.ID, "test", first[0].Seq+2); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.Cursor(ctx, pool, m.ID, "test")
	resume, _ := s.Query(ctx, Filter{MindID: m.ID, AfterSeq: cur, Ascending: true, Limit: 10})
	if len(resume) != 1 || resume[0].ID != second[0].ID {
		t.Fatalf("resume from inside a gap: %+v", resume)
	}
	// Stats count real rows, not sequence span.
	st, _ := s.Stats(ctx, m.ID, time.Time{})
	if st.Total != 2 || st.LastSeq != second[0].Seq {
		t.Fatalf("stats: %+v", st)
	}
}
