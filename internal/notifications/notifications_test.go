package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestRulesAndSuppression(t *testing.T) {
	cfg := minds.DefaultConfig()
	p := minds.DefaultPersonality("x")
	if c := Rules(events.Event{Type: events.PredictionError, Metadata: map[string]any{"confidence": 0.5, "prediction_id": "p"}}, cfg, p); len(c) != 0 {
		t.Fatal("weak miss should not notify")
	}
	c := Rules(events.Event{Type: events.PredictionError, Metadata: map[string]any{"confidence": 0.9, "prediction_id": "p"}}, cfg, p)
	if len(c) != 1 || c[0].Level != "warn" || c[0].Reason["threshold"] != 0.7 {
		t.Fatalf("miss rule wrong: %+v", c)
	}
	p.Behaviour.NotifyProactively = false
	if c := Rules(events.Event{Type: events.BeliefUpdated, Confidence: 0.95, Salience: 0.9, Metadata: map[string]any{"belief_id": "b"}}, cfg, p); len(c) != 0 {
		t.Fatal("non-proactive mind should not send info notifications")
	}

	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "n"})
	s := NewStore(pool)
	cand := Candidate{Level: "warn", Title: "t", Rule: "r", AggregateKey: "k", SourceEvent: ids.New()}
	n1, sup, err := s.Deliver(ctx, pool, m.ID, cand, time.Hour)
	if err != nil || sup {
		t.Fatalf("first delivery: %v %v", err, sup)
	}
	n2, sup, _ := s.Deliver(ctx, pool, m.ID, cand, time.Hour)
	if !sup || n2.ID != n1.ID || n2.Count != 2 {
		t.Fatalf("repeat should aggregate: %+v %v", n2, sup)
	}
	if cnt, _ := s.CountNew(ctx, m.ID); cnt != 1 {
		t.Fatalf("count new: %d", cnt)
	}
	if err := s.MarkRead(ctx, m.ID, n1.ID); err != nil {
		t.Fatal(err)
	}
	if cnt, _ := s.CountNew(ctx, ids.Nil); cnt != 0 {
		t.Fatal("still unread")
	}
	_, sup, _ = s.Deliver(ctx, pool, m.ID, cand, time.Nanosecond)
	if sup {
		t.Fatal("outside cooldown should create a new notification")
	}
}
