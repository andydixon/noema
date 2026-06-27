package predictions

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

func TestJudgeIsDeterministic(t *testing.T) {
	now := time.Now()
	p := Prediction{Criteria: Criteria{Kind: "event_match", Expect: "absent", Terms: []string{"server-01", "fail"}, MinTerms: 2}, WindowEnd: now.Add(time.Hour), Confidence: 0.8}
	if _, ok := Judge(p, now, nil, nil); ok {
		t.Fatal("should not be judgeable before the window ends")
	}
	fail := events.Event{ID: ids.New(), Type: events.Observation, Summary: "server-01 failed to boot"}
	o, ok := Judge(p, now, []events.Event{fail}, nil)
	if !ok || o.Status != StatusRefuted || len(o.Evidence) != 1 {
		t.Fatalf("absent-expectation should be refuted by a match: %+v", o)
	}
	o, ok = Judge(p, now.Add(2*time.Hour), nil, nil)
	if !ok || o.Status != StatusConfirmed {
		t.Fatalf("absent-expectation should confirm at window end: %+v", o)
	}
	occurs := p
	occurs.Criteria.Expect = "occurs"
	if o, ok := Judge(occurs, now.Add(2*time.Hour), nil, nil); !ok || o.Status != StatusRefuted {
		t.Fatalf("occurs-expectation should be refuted at window end: %+v", o)
	}
	unrelated := events.Event{Type: events.Observation, Summary: "weather is mild"}
	if _, ok := Judge(occurs, now, []events.Event{unrelated}, nil); ok {
		t.Fatal("unrelated event must not resolve")
	}
	conf := 0.9
	b := Prediction{Criteria: Criteria{Kind: "belief", BeliefID: ids.New(), MinConfidence: 0.8}, WindowEnd: now.Add(time.Hour)}
	if o, ok := Judge(b, now, nil, &conf); !ok || o.Status != StatusConfirmed {
		t.Fatalf("belief criteria wrong: %+v", o)
	}
}

func TestStoreResolveAndCalibration(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "p"})
	s := NewStore(pool)
	if _, err := s.Create(ctx, pool, New{MindID: m.ID, Statement: "x", Confidence: 1, WindowEnd: time.Now().Add(time.Hour), Criteria: Criteria{Kind: "operator"}}); err == nil {
		t.Fatal("certainty 1.0 accepted")
	}
	mk := func(conf float64) Prediction {
		p, err := s.Create(ctx, pool, New{MindID: m.ID, Statement: "server stays healthy", Confidence: conf, WindowEnd: time.Now().Add(time.Hour), Criteria: Criteria{Kind: "event_match", Expect: "absent", Terms: []string{"server", "fail"}}, Source: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1, p2, p3 := mk(0.9), mk(0.7), mk(0.3)
	if _, err := s.Resolve(ctx, pool, p1, Outcome{Status: StatusRefuted, Reason: "server failed"}, ids.Nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, pool, p1, Outcome{Status: StatusConfirmed}, ids.Nil); err == nil {
		t.Fatal("double resolution accepted")
	}
	s.Resolve(ctx, pool, p2, Outcome{Status: StatusConfirmed}, ids.Nil) //nolint:errcheck
	s.Resolve(ctx, pool, p3, Outcome{Status: StatusConfirmed}, ids.Nil) //nolint:errcheck
	c, err := s.Calibrate(ctx, m.ID, time.Now().Add(-time.Hour))
	if err != nil || c.Total != 3 || c.Confirmed != 2 || c.Refuted != 1 {
		t.Fatalf("calibration: %v %+v", err, c)
	}
	wantBrier := (0.9*0.9 + 0.3*0.3 + 0.7*0.7) / 3
	if d := c.Brier - wantBrier; d > 1e-9 || d < -1e-9 {
		t.Fatalf("brier %v want %v", c.Brier, wantBrier)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM prediction_results`); err == nil {
		t.Fatal("results must be append-only")
	}
	open, _ := s.Open(ctx, ids.Nil)
	if len(open) != 0 {
		t.Fatalf("open predictions remain: %d", len(open))
	}
	got, _ := s.Get(ctx, m.ID, p1.ID)
	if got.Status != StatusRefuted || got.ResolvedAt == nil || len(got.Resolution) == 0 {
		t.Fatalf("resolution not stored: %+v", got)
	}
}
