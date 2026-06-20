package memory

import (
	"context"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
)

func TestMemoryLifecycleRetrievalAndIsolation(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	a, _ := ms.Create(ctx, minds.CreateParams{Slug: "a"})
	b, _ := ms.Create(ctx, minds.CreateParams{Slug: "b"})
	s := NewStore(ctx, pool, nil)
	if !s.VectorsEnabled() {
		t.Log("pgvector not available in this database; vector path skipped")
	}

	mk := func(mind minds.Mind, kind concepts.MemoryKind, content string, importance float64, when time.Time) Memory {
		m, err := s.Create(ctx, pool, New{MindID: mind.ID, Kind: kind, Epistemic: concepts.Observation, Content: content, Source: "test", Trust: events.TrustUser, Confidence: 0.9, Importance: importance, OccurredAt: when})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	now := time.Now()
	e1 := mk(a, concepts.Episodic, "Andy asked for the atlas server to be checked after a latency spike", 0.7, now.Add(-48*time.Hour))
	e2 := mk(a, concepts.Episodic, "The atlas disk showed SMART CRC errors during the incident", 0.8, now.Add(-24*time.Hour))
	mk(a, concepts.Semantic, "Andy prefers Go for deployed services", 0.6, now.Add(-30*24*time.Hour))
	mk(b, concepts.Episodic, "atlas latency spike discussed with a different operator", 0.9, now)

	if _, err := s.Create(ctx, pool, New{MindID: a.ID, Kind: "weird", Epistemic: concepts.Claim, Content: "x"}); err == nil {
		t.Fatal("invalid kind accepted")
	}

	got, err := s.Retrieve(ctx, a.ID, Query{Text: "atlas latency", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Memory.ID != e1.ID {
		t.Fatalf("retrieval wrong: %+v", got)
	}
	if got[0].Components["text"] == 0 || got[0].Components["recency"] == 0 || got[0].Score <= 0 || got[0].Score > 1 {
		t.Fatalf("components missing: %+v", got[0])
	}
	for _, r := range got {
		if r.Memory.MindID != a.ID {
			t.Fatal("cross-mind leakage in retrieval")
		}
	}
	// Mind b sees only its own memory, and cannot Get a's memory.
	gotB, _ := s.Retrieve(ctx, b.ID, Query{Text: "atlas", Limit: 5})
	if len(gotB) != 1 || gotB[0].Memory.MindID != b.ID {
		t.Fatalf("isolation broken: %+v", gotB)
	}
	if _, err := s.Get(ctx, b.ID, e1.ID); err == nil {
		t.Fatal("cross-mind Get succeeded")
	}
	// Reinforcement and decay never delete.
	if err := s.Reinforce(ctx, pool, a.ID, e2.ID); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(ctx, a.ID, e2.ID)
	if m.RetrievalCount != 1 || m.LastRetrievedAt == nil {
		t.Fatalf("reinforce wrong: %+v", m)
	}
	pool.Exec(ctx, `UPDATE memories SET created_at = now() - interval '60 days', last_retrieved_at = NULL WHERE id=$1`, e1.ID) //nolint:errcheck
	if _, err := s.Decay(ctx, a.ID, 14*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	m1, _ := s.Get(ctx, a.ID, e1.ID)
	if m1.Activation >= 1 || m1.Activation < 0.01 || m1.Status != StatusActive {
		t.Fatalf("decay wrong: %+v", m1)
	}
	// Relationships are mind-scoped.
	if _, err := s.Relate(ctx, pool, a.ID, e1.ID, e2.ID, concepts.RelatedTo, 0.8, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Relate(ctx, pool, a.ID, e1.ID, gotB[0].Memory.ID, concepts.RelatedTo, 0.8, "test"); err == nil {
		t.Fatal("cross-mind relationship accepted")
	}
	rel, _ := s.Relationships(ctx, a.ID, e2.ID)
	if len(rel) != 1 || rel[0].Direction != "in" || rel[0].Other.ID != e1.ID {
		t.Fatalf("relationships wrong: %+v", rel)
	}
	// Retraction keeps the row.
	if err := s.SetStatus(ctx, a.ID, e2.ID, StatusRetracted); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx, a.ID, Filter{})
	if len(list) != 2 {
		t.Fatalf("retracted memory still listed as active: %d", len(list))
	}
	if _, err := s.Get(ctx, a.ID, e2.ID); err != nil {
		t.Fatal("retracted memory must remain readable")
	}
	st, _ := s.Stats(ctx, a.ID)
	if st.Total != 2 || st.ByKind["semantic"] != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
}
