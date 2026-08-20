package experiments

import (
	"context"
	"log/slog"
	"testing"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay/replaytest"
	"github.com/andydixon/noema/pkg/ids"
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

// TestVariantsShareStateAndCompare: two forks of one snapshot receive the
// same stimuli under different configurations; each gets its own subject,
// the comparison is computed deterministically, and the first variant
// remains the experiment's subject for the existing views.
func TestVariantsShareStateAndCompare(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	base, _ := h.Minds.Create(ctx, minds.CreateParams{Slug: "vbase", DisplayName: "Base"})
	sn, err := h.Snaps.Create(ctx, base, "baseline", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{DB: h.Pool, Minds: h.Minds, Snapshots: h.Snaps, Brain: h.Brain, Identity: h.Identity, Log: slog.New(slog.DiscardHandler)}
	def := Definition{
		Stimuli: []Stimulus{{Text: "Deployment of the payments service finished at 09:10"}, {Text: "Latency alert on the payments service at 09:12"}},
		Variants: []Variant{
			{Name: "wide", Config: map[string]any{"workspace_capacity": 7}},
			{Name: "narrow", Config: map[string]any{"workspace_capacity": 2}, LLMRoles: map[string]string{"respond": string(ids.New())}},
		},
	}
	e, err := svc.Create(ctx, "capacity comparison", "a narrow workspace attends to fewer recollections", sn.ID, def, "test")
	if err != nil {
		t.Fatal(err)
	}
	e, err = svc.Run(ctx, e.ID, "test")
	if err != nil || e.Status != "done" {
		t.Fatalf("run: %v %+v", err, e)
	}
	res := ResultsOf(e)
	if len(res.Variants) != 2 || res.Variants[0].Name != "wide" || res.Variants[1].Name != "narrow" {
		t.Fatalf("variants: %+v", res.Variants)
	}
	if res.Variants[0].Subject == res.Variants[1].Subject || res.Subject != res.Variants[0].Subject {
		t.Fatalf("subjects: %+v", res.Variants)
	}
	a, _ := h.Minds.Get(ctx, res.Variants[0].Subject)
	b, _ := h.Minds.Get(ctx, res.Variants[1].Subject)
	if a.Config.WorkspaceCapacity != 7 || b.Config.WorkspaceCapacity != 2 || len(b.Config.LLMRoles) != 1 || len(a.Config.LLMRoles) != 0 {
		t.Fatalf("overrides not applied per fork: %+v %+v", a.Config, b.Config)
	}
	if res.Comparison == nil || res.Comparison.BeliefOverlap < 0 || res.Comparison.BeliefOverlap > 1 || len(res.Comparison.Notes) == 0 {
		t.Fatalf("comparison: %+v", res.Comparison)
	}
	if len(res.Variants[0].Steps) != 2 || len(res.Variants[1].Steps) != 2 {
		t.Fatalf("each variant should have seen both stimuli: %+v", res.Variants)
	}
	// The comparison is a pure function of the results and therefore repeatable.
	again := Compare(res.Variants)
	if again.FirstDivergentStimulus != res.Comparison.FirstDivergentStimulus || again.AttentionOverlap != res.Comparison.AttentionOverlap {
		t.Fatal("comparison not deterministic")
	}
	// A single-subject definition still works and records no comparison.
	single, _ := svc.Create(ctx, "single", "", sn.ID, Definition{Stimuli: def.Stimuli}, "test")
	single, _ = svc.Run(ctx, single.ID, "test")
	if sr := ResultsOf(single); len(sr.Variants) != 1 || sr.Comparison != nil {
		t.Fatalf("single subject: %+v", sr)
	}
}
