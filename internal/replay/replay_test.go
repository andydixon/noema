package replay

import (
	"context"
	"log/slog"
	"testing"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay/replaytest"
	"github.com/andydixon/noema/pkg/events"
)

func TestReplayIsExactWithoutModel(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	src, err := h.Minds.Create(ctx, minds.CreateParams{Slug: "src", DisplayName: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	// Live run on the source, driven synchronously: one stimulus, settle.
	live, err := NewDriver(ctx, h.Brain, src, "test")
	if err != nil {
		t.Fatal(err)
	}
	sn, err := h.Snaps.Create(ctx, src, "t0", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Observe(ctx, "operator", "Andy said the payments service failed after the migration", 0.8, nil); err != nil {
		t.Fatal(err)
	}
	produced, cycles, err := live.Settle(ctx)
	if err != nil || cycles == 0 || len(Digest(produced)) == 0 {
		t.Fatalf("live run produced nothing: %v cycles=%d", err, cycles)
	}
	last, _ := h.Events.Query(ctx, evstore.Filter{MindID: src.ID, Limit: 1})
	svc := &Service{DB: h.Pool, Events: h.Events, Minds: h.Minds, Snapshots: h.Snaps, Brain: h.Brain, Log: slog.New(slog.DiscardHandler)}
	r, err := svc.Run(ctx, src, sn.ID, sn.EventSeq, last[0].Seq, "test")
	if err != nil {
		t.Fatalf("replay: %v %+v", err, r)
	}
	if r.Status != "done" || r.Summary.Steps != 1 || r.Summary.Exact != 1 || r.Summary.LLMLive != 0 {
		t.Fatalf("expected one exact step: %+v steps=%s", r.Summary, string(r.Steps))
	}
	steps := StepsOf(r)
	if len(steps) != 1 || !Same(steps[0].Original, steps[0].Replayed) || len(steps[0].Replayed) == 0 {
		t.Fatalf("step mismatch: %+v", steps)
	}
	sandbox, err := h.Minds.Get(ctx, *r.ReplayMindID)
	if err != nil || sandbox.Status != minds.StatusDisabled || sandbox.ForkedFromSnapshot == nil {
		t.Fatalf("sandbox mind wrong: %+v %v", sandbox, err)
	}
	// The source's history is untouched by the replay.
	after, _ := h.Events.Query(ctx, evstore.Filter{MindID: src.ID, Limit: 1})
	if after[0].Seq != last[0].Seq {
		t.Fatal("replay wrote into the source mind")
	}
	// Digest normalisation ignores ids and numbers.
	if Normalise("selected 3 thoughts for 01a0a148-9034-7685-9794-cf5ec0c25fb7") != "selected <n> thoughts for <id>" {
		t.Fatal(Normalise("selected 3 thoughts for 01a0a148-9034-7685-9794-cf5ec0c25fb7"))
	}
	_ = events.Observation
}
