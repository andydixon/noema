package goals

import (
	"context"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
)

func TestGoalsLifecycleAndRelevance(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	a, _ := ms.Create(ctx, minds.CreateParams{Slug: "a"})
	b, _ := ms.Create(ctx, minds.CreateParams{Slug: "b"})
	s := NewStore(pool)
	root, err := s.Create(ctx, pool, New{MindID: a.ID, Title: "Find the cause of atlas latency", Kind: KindInvestigation, Priority: 0.9, Subject: "atlas latency", Owner: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(ctx, pool, New{MindID: a.ID, ParentID: root.ID, Title: "Check disk latency on atlas", Priority: 0.7})
	if err != nil || child.ParentID != root.ID {
		t.Fatalf("child: %v %+v", err, child)
	}
	if _, err := s.Create(ctx, pool, New{MindID: b.ID, ParentID: root.ID, Title: "cross-mind child"}); err == nil {
		if got, _ := s.List(ctx, b.ID, nil, 10); len(got) != 0 {
			t.Fatal("cross-mind parent accepted")
		}
	}
	rel, id := Relevance("query latency on atlas rose sharply", []Goal{root, child})
	if rel < 0.3 || id != root.ID {
		t.Fatalf("relevance wrong: %v %v", rel, id)
	}
	if rel0, _ := Relevance("weather is mild", []Goal{root}); rel0 != 0 {
		t.Fatalf("unrelated text should score 0, got %v", rel0)
	}
	p := 0.5
	g, err := s.Apply(ctx, pool, a.ID, child.ID, Update{Progress: &p, Blockers: &[]string{"no metrics access"}})
	if err != nil || g.Status != StatusBlocked || g.Progress != 0.5 {
		t.Fatalf("apply: %v %+v", err, g)
	}
	done := StatusCompleted
	g, _ = s.Apply(ctx, pool, a.ID, child.ID, Update{Status: &done})
	if g.Progress != 1 || g.CompletedAt == nil {
		t.Fatalf("completion wrong: %+v", g)
	}
	pool.Exec(ctx, `UPDATE goals SET last_progress_at = now() - interval '2 days' WHERE id=$1`, root.ID) //nolint:errcheck
	stalled, _ := s.Stalled(ctx, a.ID, 24*time.Hour)
	if len(stalled) != 1 || stalled[0].ID != root.ID {
		t.Fatalf("stalled wrong: %+v", stalled)
	}
	if _, err := s.FindBySubject(ctx, pool, a.ID, KindInvestigation, "atlas latency"); err != nil {
		t.Fatal("subject lookup failed")
	}
	st, _ := s.Stats(ctx, a.ID)
	if st.Total != 2 || st.ByStatus["completed"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
}
