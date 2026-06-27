package motivation

import (
	"context"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
)

func TestRulesApplyDecayAndModulate(t *testing.T) {
	p := minds.DefaultPersonality("x")
	st := State{Drives: DefaultDrives(p), Affect: Baselines(p)}
	base := st.Affect
	d, ok := Rules(events.Event{Type: events.ContradictionDetected})
	if !ok {
		t.Fatal("contradiction should have a rule")
	}
	Apply(&st, d)
	if st.Affect.Concern <= base.Concern || st.Affect.Uncertainty <= base.Uncertainty {
		t.Fatalf("contradiction did not raise concern/uncertainty: %+v", st.Affect)
	}
	for i := 0; i < 50; i++ {
		Apply(&st, d)
	}
	if st.Affect.Concern > 1 || st.Affect.Uncertainty > 1 {
		t.Fatal("clamp failed")
	}
	if _, ok := Rules(events.Event{Type: events.ThoughtCandidate}); ok {
		t.Fatal("candidates must not move affect")
	}
	before := st.Affect.Concern
	Decay(&st, base, 3*time.Hour)
	if st.Affect.Concern >= before || st.Affect.Concern < base.Concern-0.01 {
		t.Fatalf("decay wrong: %v -> %v (base %v)", before, st.Affect.Concern, base.Concern)
	}
	w := map[string]float64{"novelty": 1, "goal_relevance": 1, "contradiction": 1, "salience": 1}
	for i := range st.Drives {
		if st.Drives[i].Name == "curiosity" {
			st.Drives[i].Value = 1
		}
	}
	m := Modulate(w, st)
	if m["novelty"] <= 1 || m["salience"] != 1 {
		t.Fatalf("modulation wrong: %v", m)
	}
	if k, sig := Significant(base, st.Affect); sig && k == "" {
		t.Fatal("significant without key")
	}
	// No forbidden drives can exist.
	for _, n := range DriveNames {
		if n == "self_preservation" || n == "replication" || n == "acquisition" {
			t.Fatal("forbidden drive present")
		}
	}
}

func TestStoreInitialisesResetsAndPersists(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "mot"})
	s := NewStore(pool)
	st, err := s.Load(ctx, pool, m)
	if err != nil || len(st.Drives) != len(DriveNames) {
		t.Fatalf("load: %v %+v", err, st)
	}
	st.Affect.Concern = 0.9
	s.SaveAffect(ctx, pool, m.ID, st.Affect) //nolint:errcheck
	d := st.Drives[0]
	d.Value, d.Max = 0.95, 0.8
	s.SaveDrive(ctx, pool, m.ID, d) //nolint:errcheck
	st2, _ := s.Load(ctx, pool, m)
	if st2.Affect.Concern != 0.9 || st2.Drives[0].Value != 0.8 {
		t.Fatalf("persist/clamp wrong: %+v %+v", st2.Affect, st2.Drives[0])
	}
	st3, err := s.Reset(ctx, pool, m)
	if err != nil || st3.Affect.Concern == 0.9 || st3.Drives[0].Value != st3.Drives[0].Baseline {
		t.Fatalf("reset wrong: %v %+v", err, st3)
	}
}
