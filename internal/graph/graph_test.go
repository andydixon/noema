package graph

import (
	"context"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/ids"
)

func TestGuessKindAndCanonical(t *testing.T) {
	cases := map[string]Kind{"atlas.local": System, "server-01": System, "web-api-prod": System, "Andy": Person, "report.pdf": Document, "andydixon/noema": Repository, "latency": Concept}
	for name, want := range cases {
		if got := GuessKind(name); got != want {
			t.Errorf("%s: got %s want %s", name, got, want)
		}
	}
	if Canonical("  Atlas,  ") != "atlas" {
		t.Fatal("canonical wrong")
	}
}

func TestUpsertRelateMergeAndIsolation(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	a, _ := ms.Create(ctx, minds.CreateParams{Slug: "a"})
	b, _ := ms.Create(ctx, minds.CreateParams{Slug: "b"})
	s := NewStore(pool)
	now := time.Now()
	andy, _ := s.Upsert(ctx, pool, a.ID, "", "Andy", now)
	andy2, _ := s.Upsert(ctx, pool, a.ID, "", "andy", now)
	if andy.ID != andy2.ID || andy2.MentionCount != 2 || andy.Kind != Person {
		t.Fatalf("upsert dedupe wrong: %+v %+v", andy, andy2)
	}
	atlas, _ := s.Upsert(ctx, pool, a.ID, "", "atlas", now)
	atlasHost, _ := s.Upsert(ctx, pool, a.ID, "", "atlas.local", now)
	if _, err := s.Relate(ctx, pool, a.ID, andy.ID, atlas.ID, concepts.DiscussedWith, 0.3, "co-mention", ids.Nil); err != nil {
		t.Fatal(err)
	}
	r2, _ := s.Relate(ctx, pool, a.ID, andy.ID, atlas.ID, concepts.DiscussedWith, 0.6, "co-mention", ids.Nil)
	_ = r2
	nb, _ := s.Neighbourhood(ctx, a.ID, andy.ID, 10)
	if len(nb) != 1 || nb[0].Count != 2 || nb[0].Confidence != 0.6 {
		t.Fatalf("relate reinforcement wrong: %+v", nb)
	}
	// Cross-mind endpoints are refused.
	other, _ := s.Upsert(ctx, pool, b.ID, "", "atlas", now)
	if _, err := s.Relate(ctx, pool, a.ID, andy.ID, other.ID, concepts.RelatedTo, 0.5, "x", ids.Nil); err == nil {
		t.Fatal("cross-mind edge accepted")
	}
	if _, err := s.Get(ctx, b.ID, andy.ID); err == nil {
		t.Fatal("cross-mind get")
	}
	// Merge atlas.local into atlas; old id still resolves; alias matches.
	s.Relate(ctx, pool, a.ID, atlasHost.ID, andy.ID, concepts.BelongsTo, 0.5, "x", ids.Nil) //nolint:errcheck
	s.LinkRef(ctx, pool, a.ID, atlasHost.ID, "memory", ids.New())                           //nolint:errcheck
	if err := s.Merge(ctx, a.ID, atlas.ID, atlasHost.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, a.ID, atlasHost.ID)
	if err != nil || got.ID != atlas.ID || len(got.Aliases) == 0 {
		t.Fatalf("merge resolution wrong: %v %+v", err, got)
	}
	again, _ := s.Upsert(ctx, pool, a.ID, "", "atlas.local", now)
	if again.ID != atlas.ID {
		t.Fatal("alias did not match after merge")
	}
	links, _ := s.Links(ctx, a.ID, atlas.ID, 10)
	if len(links) != 1 {
		t.Fatalf("links not moved: %d", len(links))
	}
	sg, _ := s.Subgraph(ctx, a.ID, ids.Nil, "", 50)
	if len(sg.Nodes) != 2 || len(sg.Edges) < 1 || sg.Total != 2 {
		t.Fatalf("subgraph wrong: %d nodes %d edges total %d", len(sg.Nodes), len(sg.Edges), sg.Total)
	}
	st, _ := s.Stats(ctx, a.ID)
	if st.Entities != 2 || st.ByKind["person"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
}
