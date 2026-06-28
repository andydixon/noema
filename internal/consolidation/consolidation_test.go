package consolidation

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestClusteriseIsDeterministic(t *testing.T) {
	now := time.Now()
	mk := func(s string, d time.Duration) memory.Memory {
		return memory.Memory{ID: memoryID(s), Content: s, Source: "conversation", Trust: events.TrustUser, OccurredAt: now.Add(d)}
	}
	eps := []memory.Memory{mk("admin said: Andy requested Go for project A", -72*time.Hour), mk("Andy requested Go for project B", -48*time.Hour), mk("Andy used Go for project C deployments", -24*time.Hour), mk("Andy disliked Python deployment complexity", -12*time.Hour), mk("weather is mild", -1*time.Hour)}
	c1 := Clusterise(eps, 3)
	c2 := Clusterise(eps, 3)
	if len(c1) == 0 || len(c1) != len(c2) || c1[0].Key != c2[0].Key {
		t.Fatalf("clustering unstable or empty: %+v", c1)
	}
	if len(c1[0].Episodes) < 3 || !containsTerm(c1[0].Terms, "andy") || !containsTerm(c1[0].Terms, "go") {
		t.Fatalf("expected an andy/go cluster: %+v", c1[0])
	}
	if p := Proposition(c1[0]); p == "" {
		t.Fatal("empty proposition")
	}
}

func containsTerm(ts []string, x string) bool {
	for _, t := range ts {
		if t == x {
			return true
		}
	}
	return false
}

func memoryID(s string) ids.ID { return ids.New() }

func TestRunProducesLinkedGeneralisation(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	m, _ := ms.Create(ctx, minds.CreateParams{Slug: "c"})
	mem := memory.NewStore(ctx, pool, nil)
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	svc := &Service{DB: pool, Memory: mem, Beliefs: beliefs.NewStore(pool), Events: es, Minds: ms, Log: slog.New(slog.DiscardHandler)}
	for i, text := range []string{"Andy requested Go for project A", "Andy requested Go for project B", "Andy used Go for project C", "Andy disliked Python deployment complexity"} {
		if _, err := mem.Create(ctx, pool, memory.New{MindID: m.ID, Kind: concepts.Episodic, Epistemic: concepts.Observation, Content: text, Source: "conversation", Trust: events.TrustUser, Confidence: 0.9, Importance: 0.5, OccurredAt: time.Now().Add(-time.Duration(4-i) * 24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	job, err := svc.Run(ctx, m, "operator", "test")
	if err != nil || job.Status != "done" || job.Produced == 0 {
		t.Fatalf("run: %v %+v", err, job)
	}
	sem, _ := mem.List(ctx, m.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Semantic}})
	if len(sem) != job.Produced {
		t.Fatalf("semantic memories: %d", len(sem))
	}
	rel, _ := mem.Relationships(ctx, m.ID, sem[0].ID)
	if len(rel) < 3 {
		t.Fatalf("generalisation not linked to episodes: %d", len(rel))
	}
	bs, _ := svc.Beliefs.List(ctx, m.ID, beliefs.Filter{})
	if len(bs) != job.Produced || bs[0].Confidence <= beliefs.PriorFor(concepts.Inference) {
		t.Fatalf("belief wrong: %+v", bs)
	}
	// Originals untouched; second run finds nothing new.
	eps, _ := mem.List(ctx, m.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Episodic}})
	if len(eps) != 4 {
		t.Fatal("episodes changed")
	}
	job2, _ := svc.Run(ctx, m, "operator", "test")
	if job2.Produced != 0 {
		t.Fatalf("re-run should not duplicate: %+v", job2)
	}
	if err := svc.Reject(ctx, m, sem[0].ID, "not a real pattern", "andy"); err != nil {
		t.Fatal(err)
	}
	got, _ := mem.Get(ctx, m.ID, sem[0].ID)
	if got.Status != memory.StatusRetracted {
		t.Fatal("rejected generalisation should be retracted")
	}
}
