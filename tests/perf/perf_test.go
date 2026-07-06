// Package perf seeds a mind with a realistic volume of state and times the
// hot read paths. It runs only with NOEMA_PERF=1 because seeding takes a
// while; CI runs it nightly, developers run it before touching queries.
//
//	NOEMA_PERF=1 NOEMA_TEST_DATABASE_URL=... go test -run TestHotPaths -v ./tests/perf/
package perf

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

const (
	nEvents   = 60_000
	nMemories = 6_000
	nBeliefs  = 1_500
	ceiling   = 400 * time.Millisecond // any hot read slower than this is a regression
)

var vocab = strings.Fields("payments database migration latency deploy atlas server disk cpu alert go rust python andy project service host backup replica queue cache timeout retry release rollback metric dashboard incident postmortem")

func phrase(i int) string {
	var w []string
	for k := 0; k < 8; k++ {
		w = append(w, vocab[(i*7+k*3)%len(vocab)])
	}
	return fmt.Sprintf("%s (item %d)", strings.Join(w, " "), i)
}

func TestHotPaths(t *testing.T) {
	if os.Getenv("NOEMA_PERF") == "" {
		t.Skip("set NOEMA_PERF=1 to run the performance pass")
	}
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	mem := memory.NewStore(ctx, pool, nil)
	bs := beliefs.NewStore(pool)
	m, err := ms.Create(ctx, minds.CreateParams{Slug: "perf", DisplayName: "Perf"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := ms.Create(ctx, minds.CreateParams{Slug: "noise", DisplayName: "Noise"})

	seed := time.Now()
	batch := make([]events.New, 0, 500)
	flush := func(mind ids.ID) {
		if len(batch) == 0 {
			return
		}
		if _, err := es.Append(ctx, batch...); err != nil {
			t.Fatal(err)
		}
		batch = batch[:0]
	}
	types := []events.Type{events.Observation, events.ThoughtSelected, events.MemoryRetrieved, events.BeliefUpdated, events.WorkspaceBroadcast, events.CycleCompleted}
	for i := 0; i < nEvents; i++ {
		mind := m.ID
		if i%5 == 0 {
			mind = other.ID // a second mind so scoping matters
		}
		batch = append(batch, events.New{MindID: mind, Type: types[i%len(types)], Source: "perf", OccurredAt: seed.Add(-time.Duration(nEvents-i) * time.Second), Summary: phrase(i), Salience: float64(i%10) / 10, Confidence: 0.8})
		if len(batch) == 500 {
			flush(mind)
		}
	}
	flush(m.ID)
	for i := 0; i < nMemories; i++ {
		kind := concepts.Episodic
		if i%4 == 0 {
			kind = concepts.Semantic
		}
		if _, err := mem.Create(ctx, pool, memory.New{MindID: m.ID, Kind: kind, Epistemic: concepts.Observation, Content: phrase(i), Source: "perf", Trust: events.TrustUser, Confidence: 0.7, Importance: float64(i%10) / 10, OccurredAt: seed.Add(-time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		return out[0], nil
	}
	for i := 0; i < nBeliefs; i++ {
		if _, err := bs.Create(ctx, pool, emit, beliefs.New{MindID: m.ID, Proposition: phrase(i * 3), Subject: vocab[i%len(vocab)], Epistemic: concepts.Claim, Source: "perf",
			Evidence: []beliefs.EvidenceInput{{Direction: "for", Kind: "memory", RefID: ids.New().String(), Source: "user", Weight: 0.4, Credibility: 0.8}}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seeded %d events, %d memories, %d beliefs in %s", nEvents, nMemories, nBeliefs, time.Since(seed).Round(time.Millisecond))

	type probe struct {
		name string
		fn   func() error
	}
	probes := []probe{
		{"events recent 50", func() error { _, err := es.Query(ctx, evstore.Filter{MindID: m.ID, Limit: 50}); return err }},
		{"events by type", func() error {
			_, err := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{events.BeliefUpdated}, Limit: 50})
			return err
		}},
		{"events after seq (cursor)", func() error {
			_, err := es.Query(ctx, evstore.Filter{MindID: m.ID, AfterSeq: 1000, Ascending: true, Limit: 200})
			return err
		}},
		{"events search", func() error {
			_, err := es.Query(ctx, evstore.Filter{MindID: m.ID, Search: "rollback", Limit: 50})
			return err
		}},
		{"events stats", func() error { _, err := es.Stats(ctx, m.ID, time.Time{}); return err }},
		{"memory list 50", func() error { _, err := mem.List(ctx, m.ID, memory.Filter{Limit: 50}); return err }},
		{"memory text search", func() error {
			_, err := mem.List(ctx, m.ID, memory.Filter{Search: "payments latency", Limit: 50})
			return err
		}},
		{"memory fused retrieve", func() error {
			_, err := mem.Retrieve(ctx, m.ID, memory.Query{Text: "database migration latency alert", Limit: 10})
			return err
		}},
		{"memory stats", func() error { _, err := mem.Stats(ctx, m.ID); return err }},
		{"beliefs list", func() error { _, err := bs.List(ctx, m.ID, beliefs.Filter{Limit: 50}); return err }},
		{"beliefs search", func() error { _, err := bs.Search(ctx, m.ID, "deploy rollback", 20); return err }},
		{"beliefs contested", func() error { _, err := bs.Contested(ctx, m.ID, 20); return err }},
		{"beliefs uncertain", func() error { _, err := bs.Uncertain(ctx, m.ID, 20); return err }},
		{"beliefs stats", func() error { _, err := bs.Stats(ctx, m.ID); return err }},
		{"beliefs recent changes", func() error { _, err := bs.RecentChanges(ctx, m.ID, seed.Add(-time.Hour), 50); return err }},
		{"snapshot capture", func() error { _, err := snapshots.Capture(ctx, pool, es, m); return err }},
	}
	failed := false
	for _, p := range probes {
		p.fn() //nolint:errcheck // warm the cache once; we time the steady state
		start := time.Now()
		var err error
		for i := 0; i < 3; i++ {
			err = p.fn()
		}
		d := time.Since(start) / 3
		mark := ""
		if d > ceiling {
			mark, failed = "  <-- over ceiling", true
		}
		t.Logf("%-28s %8s%s", p.name, d.Round(100*time.Microsecond), mark)
		if err != nil {
			t.Errorf("%s: %v", p.name, err)
		}
	}
	// Plans: the hot event and memory queries must be index-driven.
	plans := map[string]string{
		"events recent": `EXPLAIN SELECT id FROM cognitive_events WHERE mind_id=$1 ORDER BY seq DESC LIMIT 50`,
		"events cursor": `EXPLAIN SELECT id FROM cognitive_events WHERE mind_id=$1 AND seq > 1000 ORDER BY seq ASC LIMIT 200`,
		"memory list":   `EXPLAIN SELECT id FROM memories WHERE mind_id=$1 AND status='active' ORDER BY occurred_at DESC, id DESC LIMIT 50`,
		"beliefs list":  `EXPLAIN SELECT id FROM beliefs WHERE mind_id=$1 AND status NOT IN ('superseded') ORDER BY confidence DESC LIMIT 50`,
	}
	for name, q := range plans {
		rows, err := pool.Query(ctx, q, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var line string
			rows.Scan(&line) //nolint:errcheck
			plan = append(plan, strings.TrimSpace(line))
		}
		rows.Close()
		joined := strings.Join(plan, " | ")
		if strings.Contains(joined, "Seq Scan on cognitive_events") || strings.Contains(joined, "Seq Scan on memories") || strings.Contains(joined, "Seq Scan on beliefs") {
			t.Errorf("%s uses a sequential scan: %s", name, joined)
		}
		t.Logf("plan %-14s %s", name, plan[0])
	}
	if failed {
		t.Fatalf("a hot read exceeded %s", ceiling)
	}
}
