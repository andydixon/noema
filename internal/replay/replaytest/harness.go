// Package replaytest wires a deterministic cognitive stack for replay and
// experiment tests.
package replaytest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/capabilities/host"
	"github.com/andydixon/noema/internal/cognition"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/epistemics"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/identity"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/migrations"
)

// Harness wires the deterministic cognitive registry without a model.
type Harness struct {
	Pool     *pgxpool.Pool
	Events   *evstore.Store
	Minds    *minds.Store
	Snaps    *snapshots.Store
	Brain    brain.Config
	Identity *identity.Capturer
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
	for _, c := range host.Capabilities(host.Options{}) {
		caps.Registry.Add(c)
	}
	caps.Registry.Freeze()
	vol := volition.NewStore(pool)
	plans := planning.NewStore(pool)
	gs := goals.NewStore(pool)
	ps := predictions.NewStore(pool)
	exec := &planning.Executor{Plans: plans, Volition: vol, Caps: caps, Predictions: ps, Events: es, Minds: ms, Memory: mem, Log: log,
		Planner: &planning.Planner{Registry: caps.Registry, Effect: planning.PolicyEffect(caps)}}
	rs := research.NewStore(pool)
	ag := &agency.Service{DB: pool, Caps: caps, Plans: plans, Volition: vol, Goals: gs, Beliefs: bs, Predictions: ps, Events: es}
	reg := cognition.NewRegistry(cognition.Stores{Memory: mem, Beliefs: bs, Conversations: conversation.NewStore(pool), Graph: graph.NewStore(pool), Assertions: epistemics.NewStore(pool), Agency: ag, Research: rs})
	// No motivation hook: affect decay depends on elapsed wall-clock time,
	// which would make replay comparisons in these tests timing-dependent.
	cfg := brain.Config{DB: pool, Events: es, Minds: ms, Registry: reg, Pool: scheduler.NewPool(2), Log: log, Metrics: observability.NewMetrics(),
		Services: brain.Services{Events: es, Memory: mem, Beliefs: bs, Goals: gs, Motivation: motivation.NewStore(pool), Predictions: ps, Capabilities: caps, Volition: vol, Plans: plans, Executor: exec, Minds: ms}}
	ident := &identity.Capturer{DB: pool, Minds: ms}
	return &Harness{Pool: pool, Events: es, Minds: ms, Snaps: &snapshots.Store{DB: pool, Events: es, Minds: ms, Identity: ident}, Brain: cfg, Identity: ident}
}
