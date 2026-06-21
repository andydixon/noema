package beliefs

import (
	"context"
	"errors"
	"testing"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestBeliefLifecycleProvenanceAndContradiction(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "b"})
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		return out[0], nil
	}
	s := NewStore(pool)
	obs1s, _ := es.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "smart", Summary: "SMART CRC errors on server-01", Salience: 0.8, Confidence: 1})
	obs2s, _ := es.Append(ctx, events.New{MindID: m.ID, Type: events.Observation, Source: "metrics", Summary: "I/O latency high on server-01", Salience: 0.8, Confidence: 1})
	obs1, obs2 := obs1s[0], obs2s[0]

	res, err := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "server-01 has an intermittent storage problem", Subject: "server-01", Epistemic: concepts.Hypothesis, Source: "reasoning",
		Evidence:     []EvidenceInput{{Direction: "for", Kind: "event", RefID: string(obs1.ID), Source: "smart", Weight: 0.6, Credibility: 0.9, Note: "CRC errors"}},
		Alternatives: []string{"SATA cable issue", "controller issue"}, OriginEvent: obs1.ID})
	if err != nil {
		t.Fatal(err)
	}
	b := res.Belief
	if b.Status != StatusHypothesis && b.Status != StatusTentative || b.Confidence <= PriorFor(concepts.Hypothesis) || res.Events[0].Type != events.HypothesisCreated {
		t.Fatalf("create wrong: %+v %+v", b, res.Events)
	}
	if len(res.Events[0].Evidence) != 2 {
		t.Fatalf("provenance refs missing: %+v", res.Events[0].Evidence)
	}
	// Support from a second, independent source raises confidence more than repetition.
	r2, err := s.AddEvidence(ctx, pool, emit, m.ID, b.ID, EvidenceInput{Direction: "for", Kind: "event", RefID: string(obs2.ID), Source: "metrics", Weight: 0.6, Credibility: 0.9, Note: "latency"}, "observed latency", obs2.ID, ids.Nil)
	if err != nil || r2.Belief.Confidence <= b.Confidence || r2.Belief.Version != 2 || r2.Events[0].Type != events.HypothesisSupported {
		t.Fatalf("support wrong: %v %+v", err, r2)
	}
	// Counter-evidence lowers it and may contest.
	r3, err := s.AddEvidence(ctx, pool, emit, m.ID, b.ID, EvidenceInput{Direction: "against", Kind: "note", Source: "smart", Weight: 0.7, Credibility: 0.9, Note: "extended SMART test passed"}, "test passed", ids.Nil, ids.Nil)
	if err != nil || r3.Belief.Confidence >= r2.Belief.Confidence || r3.Events[0].Type != events.HypothesisWeakened {
		t.Fatalf("weaken wrong: %v %+v", err, r3)
	}
	vs, _ := s.Versions(ctx, m.ID, b.ID)
	if len(vs) != 3 || vs[0].Version != 3 || vs[0].Breakdown.Against == 0 {
		t.Fatalf("versions wrong: %+v", vs)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM belief_versions WHERE belief_id=$1`, b.ID); err == nil {
		t.Fatal("belief history must be append-only")
	}
	alts, _ := s.Alternatives(ctx, m.ID, b.ID)
	if len(alts) != 2 {
		t.Fatalf("alternatives missing: %+v", alts)
	}

	// A negated proposition triggers a contradiction and links both beliefs.
	rc, err := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "server-01 does not have an intermittent storage problem", Subject: "server-01", Epistemic: concepts.Claim, Source: "user",
		Evidence: []EvidenceInput{{Direction: "for", Kind: "note", Source: "user", Weight: 0.5, Credibility: 0.85}}})
	if err != nil {
		t.Fatal(err)
	}
	var sawContra bool
	for _, ev := range rc.Events {
		if ev.Type == events.ContradictionDetected {
			sawContra = true
		}
	}
	if !sawContra {
		t.Fatalf("no contradiction event: %+v", rc.Events)
	}
	links, _ := s.Links(ctx, m.ID, b.ID)
	if len(links) != 1 || links[0].Relation != "contradicts" || links[0].Other.ID != rc.Belief.ID {
		t.Fatalf("links wrong: %+v", links)
	}
	after, _ := s.Get(ctx, pool, m.ID, b.ID)
	if after.Confidence >= r3.Belief.Confidence {
		t.Fatalf("contradiction should lower confidence: %v -> %v", r3.Belief.Confidence, after.Confidence)
	}

	// Operator correction: rejected, pinned, history kept, do-not-infer blocks recreation.
	rr, err := s.Retract(ctx, emit, m.ID, b.ID, "it was the SATA cable", "andy", true)
	if err != nil || rr.Belief.Status != StatusRejected || rr.Events[0].Type != events.BeliefRetracted {
		t.Fatalf("retract wrong: %v %+v", err, rr)
	}
	got, _ := s.Get(ctx, pool, m.ID, b.ID)
	if got.Status != StatusRejected || got.PinnedStatus == nil {
		t.Fatalf("not pinned rejected: %+v", got)
	}
	vs, _ = s.Versions(ctx, m.ID, b.ID)
	if len(vs) < 4 {
		t.Fatalf("history lost on retraction: %d", len(vs))
	}
	if _, err := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "server-01 has an intermittent storage problem", Epistemic: concepts.Hypothesis}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("do-not-infer not enforced: %v", err)
	}
	cs, _ := s.Corrections(ctx, m.ID, "")
	if len(cs) != 2 {
		t.Fatalf("corrections missing: %+v", cs)
	}
	// Isolation: another mind cannot see these.
	other, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "other"})
	if _, err := s.Get(ctx, pool, other.ID, b.ID); err == nil {
		t.Fatal("cross-mind belief read")
	}
	list, _ := s.List(ctx, other.ID, Filter{})
	if len(list) != 0 {
		t.Fatal("cross-mind belief list")
	}
}
