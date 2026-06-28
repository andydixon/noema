package reflection

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
)

func TestReflectionFindsPatternsAndWritesLessons(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "r"})
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	mem := memory.NewStore(ctx, pool, nil)
	svc := &Service{DB: pool, Events: es, Beliefs: beliefs.NewStore(pool), Goals: goals.NewStore(pool), Memory: mem, Predictions: predictions.NewStore(pool), Log: slog.New(slog.DiscardHandler)}
	for i := 0; i < 2; i++ {
		es.Append(ctx, events.New{MindID: m.ID, Type: events.PredictionError, Source: "test", Summary: "prediction refuted (was 0.80): server-07 will remain healthy", Salience: 0.8, Confidence: 1, Metadata: map[string]any{"confidence": 0.8}}) //nolint:errcheck
	}
	es.Append(ctx, events.New{MindID: m.ID, Type: events.GoalCompleted, Source: "test", Summary: "goal completed: check disks", Salience: 0.5, Confidence: 1}) //nolint:errcheck
	r, err := svc.Run(ctx, m, "operator", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings.PredictionErrors) != 2 || len(r.Findings.CompletedGoals) != 1 || len(r.Findings.Patterns) == 0 || len(r.Findings.Lessons) == 0 {
		t.Fatalf("findings wrong: %+v", r.Findings)
	}
	if !strings.Contains(r.Prose, "server-07") || r.EventID.IsZero() {
		t.Fatalf("prose/event wrong: %q %v", r.Prose, r.EventID)
	}
	lessons, _ := mem.List(ctx, m.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Procedural}})
	if len(lessons) != 1 {
		t.Fatalf("expected one procedural lesson, got %d", len(lessons))
	}
	r2, _ := svc.Run(ctx, m, "operator", "test")
	if len(r2.Findings.Lessons) != 0 || r2.PeriodStart.Sub(r.PeriodEnd).Abs() > time.Millisecond {
		t.Fatalf("second run should start where the first ended and add no duplicate lesson: %+v", r2.Findings)
	}
	latest, _ := svc.Latest(ctx, m.ID)
	if latest.ID != r2.ID {
		t.Fatal("latest wrong")
	}
}
