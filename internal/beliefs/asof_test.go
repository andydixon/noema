package beliefs

import (
	"context"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/ids"
)

func TestAsOfReadsTheVersionHeldAtTheTime(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "asof"})
	s := NewStore(pool)
	b := ids.New()
	now := time.Now()
	if _, err := pool.Exec(ctx, `INSERT INTO beliefs (id, mind_id, proposition, subject, epistemic, status, confidence, prior, source) VALUES ($1,$2,'atlas-worker is healthy','atlas','claim','rejected',0.1,0.5,'test')`, b, m.ID); err != nil {
		t.Fatal(err)
	}
	for i, v := range []struct {
		status string
		conf   float64
		at     time.Time
	}{{"likely", 0.8, now.Add(-48 * time.Hour)}, {"contested", 0.45, now.Add(-12 * time.Hour)}, {"rejected", 0.1, now.Add(-time.Hour)}} {
		if _, err := pool.Exec(ctx, `INSERT INTO belief_versions (id, belief_id, mind_id, version, status, confidence, proposition, created_at) VALUES ($1,$2,$3,$4,$5,$6,'atlas-worker is healthy',$7)`, ids.New(), b, m.ID, i+1, v.status, v.conf, v.at); err != nil {
			t.Fatal(err)
		}
	}
	for at, want := range map[time.Duration]string{-24 * time.Hour: "likely", -2 * time.Hour: "contested", 0: "rejected"} {
		vs, err := s.AsOf(ctx, m.ID, "atlas-worker", now.Add(at), 5)
		if err != nil || len(vs) != 1 || vs[0].Status != want {
			t.Fatalf("as of %s: %v %+v", at, err, vs)
		}
	}
	if vs, _ := s.AsOf(ctx, m.ID, "atlas-worker", now.Add(-72*time.Hour), 5); len(vs) != 0 {
		t.Fatalf("a belief appeared before it existed: %+v", vs)
	}
}
