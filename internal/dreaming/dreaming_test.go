package dreaming

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestInWindow(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 30, 0, 0, time.Local) }
	cases := []struct {
		w    string
		h    int
		want bool
	}{{"", 12, true}, {"01-06", 3, true}, {"01-06", 6, false}, {"01-06", 0, false}, {"22-06", 23, true}, {"22-06", 2, true}, {"22-06", 12, false}, {"garbage", 3, false}, {"3-3", 15, true}}
	for _, c := range cases {
		if got := InWindow(c.w, at(c.h)); got != c.want {
			t.Errorf("InWindow(%q, %02dh) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

func TestSampleAndAssociateAreDeterministic(t *testing.T) {
	now := time.Now()
	mk := func(s, src string, d time.Duration, imp float64) memory.Memory {
		return memory.Memory{ID: ids.New(), Content: s, Source: src, Importance: imp, OccurredAt: now.Add(d)}
	}
	mems := []memory.Memory{
		mk("Deployment of the payments service failed after the database migration", "ops", -72*time.Hour, 0.7),
		mk("Andy mentioned the payments database migration was rushed last quarter", "conversation", -2*time.Hour, 0.6),
		mk("Weather was mild and the office was quiet", "conversation", -1*time.Hour, 0.2),
		mk("Lunch was pasta", "conversation", -50*time.Minute, 0.1),
	}
	seed := ids.New()
	s1, s2 := Sample(seed, mems, 3), Sample(seed, mems, 3)
	if len(s1) != 3 || s1[0].ID != s2[0].ID || s1[2].ID != s2[2].ID {
		t.Fatalf("sample unstable: %v vs %v", s1, s2)
	}
	pairs := Associate(mems, nil, 0.12)
	if len(pairs) == 0 {
		t.Fatal("expected at least one association")
	}
	if pairs[0].A.Source == pairs[0].B.Source || len(pairs[0].Shared) < 2 {
		t.Fatalf("expected the ops/conversation payments pair first, got %+v", pairs[0])
	}
	if p := Proposition(pairs[0]); p == "" {
		t.Fatal("empty proposition")
	}
	// Already-related pairs are skipped.
	if got := Associate(mems, func(a, b ids.ID) bool { return true }, 0.12); len(got) != 0 {
		t.Fatalf("related filter ignored: %d", len(got))
	}
}

func TestRunProposesLowConfidenceHypothesesAndCannotAct(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	ms := minds.NewStore(pool)
	m, _ := ms.Create(ctx, minds.CreateParams{Slug: "d"})
	m.Config.DreamHours = "" // any hour for the test
	mem := memory.NewStore(ctx, pool, nil)
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	svc := &Service{DB: pool, Memory: mem, Beliefs: beliefs.NewStore(pool), Events: es, Log: slog.New(slog.DiscardHandler)}
	seed := []struct {
		text, src string
		d         time.Duration
	}{
		{"Deployment of the payments service failed after the database migration", "ops", -72 * time.Hour},
		{"Andy mentioned the payments database migration was rushed last quarter", "conversation", -2 * time.Hour},
		{"Weather was mild and the office was quiet", "conversation", -1 * time.Hour},
	}
	for _, sd := range seed {
		if _, err := mem.Create(ctx, pool, memory.New{MindID: m.ID, Kind: concepts.Episodic, Epistemic: concepts.Observation, Content: sd.text, Source: sd.src, Trust: events.TrustUser, Confidence: 0.9, Importance: 0.6, OccurredAt: time.Now().Add(sd.d)}); err != nil {
			t.Fatal(err)
		}
	}
	// A capability service reached from inside a dream must refuse.
	caps := &capabilities.Service{Registry: capabilities.NewRegistry(), DB: pool, Events: es, Log: slog.New(slog.DiscardHandler)}
	for _, c := range capabilities.Builtins() {
		caps.Registry.Add(c)
	}
	dctx := capabilities.WithForbidden(ctx, "dreaming")
	if _, err := caps.Invoke(dctx, capabilities.Invocation{MindID: m.ID, Capability: "noema.note", Params: map[string]any{"text": "hi"}}); err != capabilities.ErrForbiddenContext {
		t.Fatalf("dream context invoked a capability: %v", err)
	}

	d, err := svc.Run(ctx, m, "operator", "test")
	if err != nil || d.Status != "done" {
		t.Fatalf("run: %+v %v", d, err)
	}
	if d.Sampled != 3 || d.Associations == 0 || d.Hypotheses == 0 {
		t.Fatalf("expected associations and hypotheses: %+v", d)
	}
	hyps, _ := svc.Hypotheses(ctx, m.ID, "proposed", 10)
	if len(hyps) != d.Hypotheses {
		t.Fatalf("hypotheses not recorded: %d vs %d", len(hyps), d.Hypotheses)
	}
	for _, h := range hyps {
		if h.Confidence > 0.35 {
			t.Fatalf("dream hypothesis started too confident: %.2f %s", h.Confidence, h.Proposition)
		}
		b, err := svc.Beliefs.Get(ctx, pool, m.ID, h.BeliefID)
		if err != nil || b.Epistemic != concepts.Hypothesis || b.Source != "dreaming" {
			t.Fatalf("belief missing or mislabelled: %+v %v", b, err)
		}
	}
	// Scheduled dreams respect hours and the kill switch.
	m.Config.DreamHours = "99-99"
	if r, _ := svc.Run(ctx, m, "schedule", "noemad"); r.Status != "rejected" {
		t.Fatalf("bad hours window should reject: %+v", r)
	}
	m.Config.DreamHours = ""
	SetPaused(true)
	if r, _ := svc.Run(ctx, m, "schedule", "noemad"); r.Status != "rejected" {
		t.Fatalf("paused should reject: %+v", r)
	}
	SetPaused(false)

	// Promote raises confidence through evidence; reject retracts.
	h0 := hyps[0]
	p, err := svc.Promote(ctx, m, h0.ID, "plausible", "andy")
	if err != nil || p.Status != "promoted" || p.Confidence <= h0.Confidence {
		t.Fatalf("promote: %+v %v", p, err)
	}
	if _, err := svc.Promote(ctx, m, h0.ID, "again", "andy"); err == nil {
		t.Fatal("double promote should fail")
	}
	if len(hyps) > 1 {
		r, err := svc.Reject(ctx, m, hyps[1].ID, "coincidence", "andy")
		if err != nil || r.Status != "rejected" {
			t.Fatalf("reject: %+v %v", r, err)
		}
		b, _ := svc.Beliefs.Get(ctx, pool, m.ID, hyps[1].BeliefID)
		if b.Status != beliefs.StatusRejected {
			t.Fatalf("rejected belief still %s", b.Status)
		}
	}
	// Dreaming the same memories again finds nothing new: pairs are now related.
	d2, _ := svc.Run(ctx, m, "operator", "test")
	if d2.Associations != 0 {
		t.Fatalf("second dream re-associated linked memories: %+v", d2)
	}
}
