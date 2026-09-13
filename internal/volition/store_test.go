package volition

import (
	"context"
	"errors"
	"testing"
	"time"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestStoreLifecycle(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "vol"})
	es := evstore.NewStore(pool, nil, nil)
	s := NewStore(pool)
	r := s.Direct(es, "volition.test")
	count := func(typ events.Type) int {
		evs, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{typ}})
		return len(evs)
	}

	cand := Candidate{Subject: "understand:prediction:p1", Description: "understand why the prediction failed", Origin: OriginPredictionError, Strength: 0.5, Urgency: 0.4, Drives: []string{"curiosity"}}
	w, created, err := s.UpsertWant(ctx, r, m.ID, cand)
	if err != nil || !created {
		t.Fatalf("create: %v %v", err, created)
	}
	w2, created, err := s.UpsertWant(ctx, r, m.ID, cand)
	if err != nil || created || w2.ID != w.ID || w2.Strength <= w.Strength || w2.Reinforcements != 1 {
		t.Fatalf("identical want should reinforce: %v %+v", err, w2)
	}
	if n := count(events.WantCreated); n != 1 {
		t.Fatalf("want_created events: %d", n)
	}
	if err := s.SuppressWant(ctx, r, w2, "forbidden by commitment", []ids.ID{ids.New()}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetWant(ctx, nil, m.ID, w.ID)
	if got.Status != WantActive || got.Suppressions != 1 {
		t.Fatalf("suppressed want must stay active: %+v", got)
	}

	avoid, _, _ := s.UpsertWant(ctx, r, m.ID, Candidate{Subject: "avoid:disruption:atlas", Description: "avoid disrupting atlas", Origin: OriginValue, Strength: 0.5, Valence: "avoid"})
	res := ResolveConflict(got, avoid, time.Now(), nil, nil)
	c1, isNew, err := s.RecordConflict(ctx, r, got, avoid, "value", res)
	if err != nil || !isNew {
		t.Fatalf("conflict: %v", err)
	}
	if _, isNew, _ := s.RecordConflict(ctx, r, avoid, got, "value", res); isNew {
		t.Fatal("same open conflict recorded twice")
	}
	if ds, _ := s.Decisions(ctx, m.ID, DecisionFilter{Kind: "conflict_resolution"}); len(ds) != 1 || ds[0].ConflictID != c1.ID {
		t.Fatalf("conflict decision: %+v", ds)
	}

	no := false
	c, err := s.CreateCommitment(ctx, r, NewCommitment{MindID: m.ID, Subject: "production changes", Description: "do not modify production without approval", Kind: KindOperatorInstruction, Polarity: "refrain", Unless: "approval",
		Matcher: Matcher{Access: "write", Targets: []string{"atlas-*"}}, Revocable: &no, CreatedBy: "andy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCommitment(ctx, r, NewCommitment{MindID: m.ID, Description: "x", Kind: "vow", Polarity: "do"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown kind accepted")
	}
	if _, err := s.CreateCommitment(ctx, r, NewCommitment{MindID: m.ID, Description: "x", Kind: KindPromise, Polarity: "do", Unless: "approval"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unless on a do-commitment accepted")
	}
	c2, err := s.CreateCommitment(ctx, r, NewCommitment{MindID: m.ID, Subject: "Production changes", Description: "do not modify production without two approvals", Kind: KindOperatorInstruction, Polarity: "refrain", Unless: "approval", Matcher: Matcher{Access: "write"}})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := s.GetCommitment(ctx, nil, m.ID, c.ID)
	if old.Status != "superseded" || old.SupersededBy != c2.ID {
		t.Fatalf("supersession: %+v", old)
	}
	if _, err := s.CloseCommitment(ctx, r, m.ID, c2.ID, "revoked", "mind changed its mind", false); err != nil {
		t.Fatalf("revocable commitment: %v", err)
	}
	c3, _ := s.CreateCommitment(ctx, r, NewCommitment{MindID: m.ID, Description: "keep customer data confidential", Kind: KindValueRule, Polarity: "refrain", Revocable: &no})
	if _, err := s.CloseCommitment(ctx, r, m.ID, c3.ID, "revoked", "inconvenient", false); !errors.Is(err, ErrNotRevocable) {
		t.Fatal("mind revoked a non-revocable commitment")
	}
	if _, err := s.CloseCommitment(ctx, r, m.ID, c3.ID, "revoked", "operator decision", true); err != nil {
		t.Fatalf("operator revoke: %v", err)
	}

	promise, _ := s.CreateCommitment(ctx, r, NewCommitment{MindID: m.ID, Description: "deliver the report", Kind: KindPromise, Polarity: "do"})
	it, err := s.CreateIntention(ctx, r, Intention{MindID: m.ID, Description: "investigate the failed prediction", Kind: IntentInvestigate, SourceWants: []ids.ID{w.ID}, CommitmentID: promise.ID, Priority: 0.7, Confidence: 0.6,
		Formation: map[string]any{"score": 0.5}})
	if err != nil || it.FormedEvent.IsZero() {
		t.Fatalf("intention: %v %+v", err, it)
	}
	it, err = s.Apply(ctx, r, m.ID, it.ID, Reconsideration{Outcome: Block, BlockedKind: BlockApproval, Reason: "logs.query requires operator approval"}, Trigger{Kind: TriggerCapabilityChanged})
	if err != nil || it.Status != IntentionBlocked || it.BlockedKind != BlockApproval {
		t.Fatalf("block: %v %+v", err, it)
	}
	it, _ = s.Apply(ctx, r, m.ID, it.ID, Reconsideration{Outcome: Resume, Reason: "granted"}, Trigger{Kind: TriggerRequestGranted})
	it, err = s.Apply(ctx, r, m.ID, it.ID, Reconsideration{Outcome: Fulfil, Reason: "plan completed"}, Trigger{Kind: TriggerPlanCompleted})
	if err != nil || it.Status != IntentionFulfilled || it.ClosedAt == nil || it.Reconsiderations != 3 {
		t.Fatalf("fulfil: %v %+v", err, it)
	}
	if got, _ := s.GetWant(ctx, nil, m.ID, w.ID); got.Status != WantSatisfied {
		t.Fatalf("source want not satisfied: %+v", got)
	}
	if got, _ := s.GetCommitment(ctx, nil, m.ID, promise.ID); got.Status != "fulfilled" {
		t.Fatalf("commitment not fulfilled: %+v", got)
	}
	if _, err := s.Apply(ctx, r, m.ID, it.ID, Reconsideration{Outcome: Resume}, Trigger{Kind: TriggerOperator}); !errors.Is(err, ErrClosed) {
		t.Fatal("closed intention reopened")
	}
	if ds, _ := s.Decisions(ctx, m.ID, DecisionFilter{IntentionID: it.ID}); len(ds) != 3 {
		t.Fatalf("reconsideration decisions: %d", len(ds))
	}
	if _, err := pool.Exec(ctx, `UPDATE volition_decisions SET outcome='rewritten'`); err == nil {
		t.Fatal("decisions must be append-only")
	}
	if n := count(events.IntentionFulfilled); n != 1 {
		t.Fatalf("intention_fulfilled events: %d", n)
	}
	// Decay and cap.
	s.UpsertWant(ctx, Recorder{Q: pool, Now: time.Now().Add(-48 * time.Hour)}, m.ID, Candidate{Subject: "old", Description: "an old want", Origin: OriginCuriosity, Strength: 0.4}) //nolint:errcheck
	if n, err := s.ExpireWants(ctx, r, m.ID, 40); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if err := s.SettleConflicts(ctx, pool, m.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Conflicts(ctx, m.ID, "open", 10); len(cs) != 0 {
		t.Fatalf("conflict with a satisfied want still open: %+v", cs)
	}
}
