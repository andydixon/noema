package experiments

import (
	"context"
	"log/slog"
	"testing"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay/replaytest"
)

func TestExperimentRunsInSandbox(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	base, err := h.Minds.Create(ctx, minds.CreateParams{Slug: "base", DisplayName: "Base"})
	if err != nil {
		t.Fatal(err)
	}
	sn, err := h.Snaps.Create(ctx, base, "baseline", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{DB: h.Pool, Minds: h.Minds, Snapshots: h.Snaps, Brain: h.Brain, Log: slog.New(slog.DiscardHandler)}
	def := Definition{
		Config:      map[string]any{"workspace_capacity": 3},
		Personality: map[string]any{"traits": map[string]any{"curiosity": 0.9}},
		Stimuli:     []Stimulus{{Text: "Deployment failed at 09:10"}, {Text: "Latency alert at 09:12 on the same service"}},
		Expected:    []Expectation{{EventType: "thought_selected"}, {EventType: "tool_executed", Absent: true}},
	}
	if _, err := svc.Create(ctx, "", "", sn.ID, def, "test"); err == nil {
		t.Fatal("nameless experiment accepted")
	}
	e, err := svc.Create(ctx, "capacity 3", "smaller workspace still links the two alerts", sn.ID, def, "test")
	if err != nil {
		t.Fatal(err)
	}
	e, err = svc.Run(ctx, e.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "done" || e.MindID == nil {
		t.Fatalf("run: %+v", e)
	}
	res := ResultsOf(e)
	if len(res.Steps) != 2 || res.Cycles == 0 || len(res.Expectations) != 2 || !res.Expectations[0].Met || !res.Expectations[1].Met {
		t.Fatalf("results: %+v", res)
	}
	subject, _ := h.Minds.Get(ctx, *e.MindID)
	if subject.Status != minds.StatusDisabled || subject.Config.WorkspaceCapacity != 3 || subject.Personality.Traits["curiosity"] != 0.9 {
		t.Fatalf("overrides not applied: %+v %+v", subject.Config.WorkspaceCapacity, subject.Personality.Traits)
	}
	if base2, _ := h.Minds.Get(ctx, base.ID); base2.Config.WorkspaceCapacity == 3 {
		t.Fatal("baseline modified")
	}
	out, err := svc.Export(ctx, e.ID)
	if err != nil || out["subject_archive"] == nil {
		t.Fatalf("export: %v", err)
	}
	// Bad override is reported, not silently ignored.
	bad, _ := svc.Create(ctx, "bad", "", sn.ID, Definition{Config: map[string]any{"nope": 1}, Stimuli: def.Stimuli}, "test")
	if bad, _ = svc.Run(ctx, bad.ID, "test"); bad.Status != "failed" || bad.Error == "" {
		t.Fatalf("bad override: %+v", bad)
	}
}
