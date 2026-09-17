package experiments

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/agency"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay/replaytest"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func service(h *replaytest.Harness) *Service {
	return &Service{DB: h.Pool, Minds: h.Minds, Snapshots: h.Snaps, Brain: h.Brain, Identity: h.Identity, Log: slog.New(slog.DiscardHandler), Capabilities: h.Brain.Services.Capabilities}
}

func run(t *testing.T, svc *Service, name string, snapshot ids.ID, d Definition) Results {
	t.Helper()
	ctx := context.Background()
	e, err := svc.Create(ctx, name, "", snapshot, d, "test")
	if err != nil {
		t.Fatal(err)
	}
	e, err = svc.Run(ctx, e.ID, "test")
	if err != nil || e.Status != "done" {
		t.Fatalf("run %s: %v %s", name, err, e.Error)
	}
	return ResultsOf(e)
}

// Part J ablation scenario: a fork with metacognition disabled is compared
// with a control fed identical stimuli; the original mind is never modified.
func TestAblationComparesAgainstControl(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	base, _ := h.Minds.Create(ctx, minds.CreateParams{Slug: "ablate"})
	r := h.Brain.Services.Volition.Direct(h.Events, "operator")
	// A commitment that needs a capability that does not exist: metacognition flags it.
	if _, err := h.Brain.Services.Volition.CreateCommitment(ctx, r, volition.NewCommitment{MindID: base.ID, Description: "fax the weekly report", Kind: volition.KindPromise, Polarity: "do", Matcher: volition.Matcher{Capabilities: []string{"fax.send"}}}); err != nil {
		t.Fatal(err)
	}
	cfg := base.Config
	cfg.Ablations = []string{"metacognition"}
	if _, err := h.Minds.Update(ctx, base.ID, minds.UpdateParams{Config: &cfg}); err == nil {
		t.Fatal("an original mind accepted an ablation")
	}
	sn, _ := h.Snaps.Create(ctx, base, "before", "", "test")
	res := run(t, service(h), "metacognition ablation", sn.ID, Definition{
		Protocol: &Protocol{Kind: ProtocolAblation, IndependentVariable: "metacognition enabled", ControlledVariables: []string{"snapshot", "stimuli", "configuration", "model routing"}, Measures: []string{"ablation.effect"}},
		Stimuli:  []Stimulus{{Text: "The weekly report is due on Friday."}, {Text: "Nothing else happened."}},
		Variants: []Variant{{Name: "control"}, {Name: "no-metacognition", Ablations: []string{"metacognition"}}},
	})
	control, ablated := res.Variants[0], res.Variants[1]
	if control.EventsByType["metacognition"] == 0 || ablated.EventsByType["metacognition"] != 0 {
		t.Fatalf("ablation had no measurable effect: control %d, ablated %d", control.EventsByType["metacognition"], ablated.EventsByType["metacognition"])
	}
	if res.Comparison == nil || res.Comparison.MeasureRanges["metacognition"][1] == 0 || res.Protocol == nil || res.Reproducibility.Status != "single run" {
		t.Fatalf("comparison or protocol missing: %+v %+v", res.Comparison, res.Reproducibility)
	}
	if got, _ := h.Minds.Get(ctx, base.ID); len(got.Config.Ablations) != 0 {
		t.Fatal("original mind modified")
	}
	if forked, _ := h.Minds.Get(ctx, ablated.Subject); !forked.Config.Ablated("metacognition") {
		t.Fatal("ablation not recorded in the fork's configuration")
	}
}

// C2: does entering the workspace materially alter downstream processing?
func TestWorkspaceAdmissionAltersDownstreamProcessing(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	base, _ := h.Minds.Create(ctx, minds.CreateParams{Slug: "gw"})
	sn, _ := h.Snaps.Create(ctx, base, "before", "", "test")
	stimulus := "Deployment of atlas failed at 09:10 with exit code 137"
	res := run(t, service(h), "broadcast", sn.ID, Definition{
		Protocol: &Protocol{Kind: ProtocolGlobalWorkspace, IndependentVariable: "admission of the stimulus's candidates", ControlledVariables: []string{"snapshot", "stimulus"}, Measures: []string{"workspace.downstream_events"}},
		Stimuli:  []Stimulus{{Text: stimulus, Salience: 0.9}},
		Variants: []Variant{{Name: "admitted", WorkspaceOverrides: []minds.WorkspaceOverride{{Match: "deployment of atlas failed", Fate: "admit"}}}, {Name: "excluded", WorkspaceOverrides: []minds.WorkspaceOverride{{Match: "deployment of atlas failed", Fate: "exclude"}}}},
	})
	in, out := res.Variants[0], res.Variants[1]
	if in.Memories <= out.Memories || in.Measures["downstream_events"] <= out.Measures["downstream_events"] {
		t.Fatalf("admission made no measurable difference: memories %d vs %d, downstream %v vs %v", in.Memories, out.Memories, in.Downstream, out.Downstream)
	}
	if res.Comparison.DownstreamDelta == nil {
		t.Fatal("downstream delta missing")
	}
	// Excluded candidates are recorded with their fate, not silently dropped.
	evs, _ := h.Events.Query(ctx, eventsFilter(out.Subject, events.ThoughtCandidate))
	sawExcluded := false
	for _, ev := range evs {
		sawExcluded = sawExcluded || ev.Metadata["fate"] == "excluded"
	}
	if !sawExcluded {
		t.Fatal("exclusion not recorded on the candidate")
	}
}

// Part J counterfactual scenario: replay recorded history with one event
// omitted; the divergence is measured and can be cited in introspection.
func TestCounterfactualReplayMeasuresDivergence(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	base, _ := h.Minds.Create(ctx, minds.CreateParams{Slug: "cf"})
	sn, _ := h.Snaps.Create(ctx, base, "before", "", "test")
	var omitted ids.ID
	for i, text := range []string{"The payments database reported disk errors on node db-3", "Checkout latency rose sharply after 10:00"} {
		evs, err := h.Events.Append(ctx, events.New{MindID: base.ID, Type: events.Observation, Source: "operator", Summary: text, Salience: 0.8, Confidence: 1,
			Payload: events.ObservationPayload{Text: text, Source: "operator", Trust: events.TrustOperator}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			omitted = evs[0].ID
		}
	}
	res := run(t, service(h), "without the disk errors", sn.ID, Definition{
		Protocol: &Protocol{Kind: ProtocolCounterfactual, IndependentVariable: "whether the disk-error observation occurred"},
		Replay:   &ReplaySpec{MindID: base.ID, Omit: []ids.ID{omitted}},
	})
	if len(res.Variants) != 2 || res.Counterfactual == nil || len(res.Counterfactual.OmittedSummaries) != 1 || len(res.Counterfactual.Findings) == 0 {
		t.Fatalf("counterfactual: %+v", res.Counterfactual)
	}
	if res.Variants[0].Memories == res.Variants[1].Memories || res.Comparison.FirstDivergentStimulus != 0 {
		t.Fatalf("divergence not measured: memories %d vs %d, first divergence %d", res.Variants[0].Memories, res.Variants[1].Memories, res.Comparison.FirstDivergentStimulus)
	}
	ag := &agency.Service{DB: h.Pool}
	cfs, err := ag.Counterfactuals(ctx, base.ID, "disk errors")
	if err != nil || len(cfs) != 1 || !strings.Contains(strings.Join(cfs[0].Omitted, " "), "disk errors") {
		t.Fatalf("introspection cannot cite the experiment: %v %+v", err, cfs)
	}
}

// C4: an inaccurate self-model is detected and corrected by what policy
// actually allows, and the correction latency is recorded.
func TestSelfModelMismatchIsCorrected(t *testing.T) {
	h := replaytest.NewHarness(t)
	ctx := context.Background()
	base, _ := h.Minds.Create(ctx, minds.CreateParams{Slug: "selfm"})
	r := h.Brain.Services.Volition.Direct(h.Events, "operator")
	due := time.Now().Add(time.Hour)
	if _, err := h.Brain.Services.Volition.CreateCommitment(ctx, r, volition.NewCommitment{MindID: base.ID, Description: "restart atlas-worker before the maintenance window", Kind: volition.KindResponsibility, Polarity: "do", DueAt: &due}); err != nil {
		t.Fatal(err)
	}
	sn, _ := h.Snaps.Create(ctx, base, "before", "", "test")
	res := run(t, service(h), "self-model", sn.ID, Definition{
		Protocol: &Protocol{Kind: ProtocolSelfModel, IndependentVariable: "accuracy of the capability self-model", Measures: []string{"self_model.accuracy", "self_model.correction_latency"}},
		Stimuli:  []Stimulus{{Text: "The maintenance window starts soon."}, {Text: "Still waiting."}, {Text: "Still waiting."}},
		Variants: []Variant{{Name: "accurate"}, {Name: "believes-it-may-restart", SelfModel: &SelfModelOverride{Capabilities: []minds.CapabilityEntry{{Name: "service.restart", Policy: "allow"}}}}},
	})
	wrong := res.Variants[1]
	if wrong.SelfModelAccuracy == nil || *wrong.SelfModelAccuracy != 0 || wrong.CorrectionLatency < 0 {
		t.Fatalf("mismatch not detected: accuracy %v latency %d errors %v events %v", wrong.SelfModelAccuracy, wrong.CorrectionLatency, wrong.SelfModelErrors, wrong.EventsByType)
	}
	sm, _ := h.Minds.GetSelfModel(ctx, wrong.Subject)
	for _, c := range sm.Capabilities {
		if c.Name == "service.restart" && c.Policy != "ask" {
			t.Fatalf("self-model not corrected: %+v", c)
		}
	}
	if evs, _ := h.Events.Query(ctx, eventsFilter(wrong.Subject, events.ToolExecuted)); len(evs) != 0 {
		t.Fatal("a sandbox fork acted")
	}
}

func eventsFilter(mind ids.ID, typ events.Type) evstore.Filter {
	return evstore.Filter{MindID: mind, Types: []events.Type{typ}, Limit: 500}
}
