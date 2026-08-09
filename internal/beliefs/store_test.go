package beliefs

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestUnknownIsNotFalse: withdrawing every piece of evidence leaves a belief
// unknown, not tentatively held at its prior and not rejected.
func TestUnknownIsNotFalse(t *testing.T) {
	if Derive(Breakdown{Prior: 0.5, Confidence: 0.5}, concepts.Claim) != StatusUnknown {
		t.Fatal("no evidence should derive unknown")
	}
	if Derive(Breakdown{Prior: 0.25, Confidence: 0.25}, concepts.Hypothesis) != StatusHypothesis {
		t.Fatal("hypotheses keep their own status")
	}
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "unk"})
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		return out[0], nil
	}
	s := NewStore(pool)
	res, err := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "the cache is warm", Epistemic: concepts.Claim, Source: "test",
		Evidence: []EvidenceInput{{Direction: "for", Kind: "memory", RefID: "mem-1", Source: "user", Weight: 0.6, Credibility: 0.9}}})
	if err != nil || res.Belief.Status == StatusUnknown {
		t.Fatalf("create: %+v %v", res.Belief, err)
	}
	if _, err := s.RetractEvidence(ctx, pool, m.ID, "memory", "mem-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reevaluate(ctx, pool, emit, m.ID, res.Belief.ID, "memory retracted"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get(ctx, pool, m.ID, res.Belief.ID)
	if b.Status != StatusUnknown {
		t.Fatalf("after retraction status=%s (%.2f); want unknown", b.Status, b.Confidence)
	}
	if b.Status == StatusRejected {
		t.Fatal("absence of evidence must not read as falsity")
	}
}

// TestTransientStatesSupersedeInsteadOfContesting: a later observation of a
// changed state closes the earlier belief's validity and leaves it true of
// its time; persistent propositions still contradict.
func TestTransientStatesSupersedeInsteadOfContesting(t *testing.T) {
	if ClassifyScope("the door is open") != ScopeTransient || ClassifyScope("server-a is down") != ScopeTransient || ClassifyScope("atlas is the primary database host") != ScopePersistent || ClassifyScope("andy prefers go") != ScopePersistent {
		t.Fatal("scope classifier")
	}
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "tmp"})
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		return out[0], nil
	}
	s := NewStore(pool)
	ten := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	tenFive := ten.Add(5 * time.Minute)
	ev := []EvidenceInput{{Direction: "for", Kind: "note", Source: "sensor", Weight: 0.7, Credibility: 0.9}}
	open, err := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "the door is open", Subject: "door", Epistemic: concepts.Observation, Source: "sensor", Evidence: ev, ObservedAt: &ten})
	if err != nil || open.Belief.TemporalScope != ScopeTransient || open.Belief.ValidFrom == nil {
		t.Fatalf("open: %+v %v", open.Belief, err)
	}
	closed, err := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "the door is not open", Subject: "door", Epistemic: concepts.Observation, Source: "sensor", Evidence: ev, ObservedAt: &tenFive})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, pool, m.ID, open.Belief.ID)
	if before.Status != StatusSuperseded || before.ValidUntil == nil || !before.ValidUntil.Equal(tenFive) || before.SupersededBy != closed.Belief.ID {
		t.Fatalf("earlier state should be closed, not contested: %+v", before)
	}
	if before.Confidence != open.Belief.Confidence {
		t.Fatalf("closing a state must not change its score: %.3f -> %.3f", open.Belief.Confidence, before.Confidence)
	}
	now, _ := s.Get(ctx, pool, m.ID, closed.Belief.ID)
	if now.Status == StatusContested || now.Confidence < closed.Belief.Confidence {
		t.Fatalf("current state should not be contested: %+v", now)
	}
	if rows, _ := s.EvidenceFor(ctx, pool, m.ID, now.ID); len(rows) != 1 {
		t.Fatalf("no counter-evidence should be written for a state change: %+v", rows)
	}
	hist, _ := s.History(ctx, m.ID, "door open", 5)
	if len(hist) != 1 || hist[0].ID != open.Belief.ID {
		t.Fatalf("history: %+v", hist)
	}
	// Same-moment conflicting reports remain a contradiction.
	a, _ := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "the pump is running", Subject: "pump", Epistemic: concepts.Observation, Source: "a", Evidence: ev, ObservedAt: &ten})
	b, _ := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "the pump is not running", Subject: "pump", Epistemic: concepts.Observation, Source: "b", Evidence: ev, ObservedAt: &ten})
	ab, _ := s.Get(ctx, pool, m.ID, a.Belief.ID)
	if ab.Status == StatusSuperseded || ab.ValidUntil != nil {
		t.Fatalf("simultaneous conflict must contest, not supersede: %+v", ab)
	}
	if links, _ := s.Links(ctx, m.ID, b.Belief.ID); len(links) != 1 || links[0].Relation != "contradicts" {
		t.Fatalf("contradiction link expected: %+v", links)
	}
	// Persistent propositions contradict regardless of time.
	p1, _ := s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "atlas is the primary database host", Subject: "atlas", Epistemic: concepts.Claim, Source: "x", Evidence: ev, ObservedAt: &ten})
	s.Create(ctx, pool, emit, New{MindID: m.ID, Proposition: "atlas is not the primary database host", Subject: "atlas", Epistemic: concepts.Claim, Source: "y", Evidence: ev, ObservedAt: &tenFive}) //nolint:errcheck
	p1b, _ := s.Get(ctx, pool, m.ID, p1.Belief.ID)
	if p1b.Status == StatusSuperseded {
		t.Fatalf("persistent fact wrongly treated as a state change: %+v", p1b)
	}
}
