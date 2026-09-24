package ui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/experiments"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/pkg/ids"
)

// The experiment page renders protocol, reproducibility, research measures
// and counterfactual findings without template errors.
func TestExperimentTemplateRendersResearchResults(t *testing.T) {
	u, err := New(app.Deps{Config: config.Defaults(), Log: slog.New(slog.DiscardHandler)}, Actions{})
	if err != nil {
		t.Fatal(err)
	}
	acc := 0.5
	res := experiments.Results{Variants: []experiments.VariantResult{
		{Name: "admitted", Wants: []string{"a"}, Intentions: []string{}, Downstream: map[string]int{"memory": 2}, IntrospectionAccuracy: &acc, SelfModelAccuracy: &acc, CorrectionLatency: 1, LLMCalls: map[string]int{}, EventsByType: map[string]int{}},
		{Name: "excluded", Ablations: []string{"metacognition"}, CorrectionLatency: -1, LLMCalls: map[string]int{}, EventsByType: map[string]int{}},
	}, Comparison: &experiments.Comparison{FirstDivergentStimulus: 0, DownstreamDelta: map[string]int{"memory": 2}, ArchitectureShare: 0.4},
		Reproducibility: experiments.Reproducibility{Status: "single run", Digest: "abc"},
		Counterfactual:  &experiments.CounterfactualReport{OmittedSummaries: []string{"disk errors"}, Findings: []string{"the belief X was never formed"}}}
	raw, _ := json.Marshal(res)
	e := experiments.Experiment{ID: ids.New(), Name: "gw", Hypothesis: "admission matters", Status: "done", Results: raw,
		Definition: experiments.Definition{Protocol: &experiments.Protocol{Kind: "global_workspace", IndependentVariable: "admission", ControlledVariables: []string{"stimulus"}, Measures: []string{"workspace.downstream_events"}}}}
	var buf bytes.Buffer
	err = u.pages["experiment"].ExecuteTemplate(&buf, "layout", View{Principal: &auth.Principal{User: auth.User{Username: "watcher", Role: auth.RoleObserver}}, Data: map[string]any{"E": e, "Results": experiments.ResultsOf(e), "Snapshot": snapshots.Snapshot{}, "Base": minds.Mind{}, "Subject": nil, "DefinitionJSON": "{}"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Independent variable", "single run", "Counterfactual", "the belief X was never formed", "Architecture share", "metacognition"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
