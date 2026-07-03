// Package replaytest wires a deterministic cognitive stack for replay and
// experiment tests.
package replaytest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/cognition"
	"github.com/andydixon/noema/internal/conversation"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
)

// Harness wires the deterministic cognitive registry without a model.
type Harness struct {
	Pool   *pgxpool.Pool
	Events *evstore.Store
	Minds  *minds.Store
	Snaps  *snapshots.Store
	Brain  brain.Config
}

// NewHarness is shared with the experiments tests.
func NewHarness(t *testing.T) *Harness {
	t.Helper()
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	es := evstore.NewStore(pool, evstore.NewBus(64), nil)
	ms := minds.NewStore(pool)
	mem := memory.NewStore(ctx, pool, nil)
	bs := beliefs.NewStore(pool)
	caps := &capabilities.Service{Registry: capabilities.NewRegistry(), DB: pool, Events: es, Log: log}
	for _, c := range capabilities.Builtins() {
		caps.Registry.Add(c)
	}
	reg := brain.NewRegistry().AddProcess(cognition.Attend{}).AddProcess(cognition.Retrieve{}).AddProcess(cognition.Associate{}).AddProcess(cognition.Perceive{}).
		AddProcess(cognition.GoalMonitor{}).AddProcess(cognition.Curiosity{}).AddProcess(cognition.Metacognition{}).AddProcess(cognition.Act{})
	cognition.Handlers{Memory: mem, Beliefs: bs, Conversations: conversation.NewStore(pool), Graph: graph.NewStore(pool)}.Register(reg)
	cfg := brain.Config{DB: pool, Events: es, Minds: ms, Registry: reg, Pool: scheduler.NewPool(2), Log: log, Metrics: observability.NewMetrics(),
		Services: brain.Services{Events: es, Memory: mem, Beliefs: bs, Goals: goals.NewStore(pool), Motivation: motivation.NewStore(pool), Predictions: predictions.NewStore(pool), Capabilities: caps}}
	return &Harness{Pool: pool, Events: es, Minds: ms, Snaps: &snapshots.Store{DB: pool, Events: es, Minds: ms}, Brain: cfg}
}
