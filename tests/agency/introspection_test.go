package agency

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/tests/harness"
)

type scriptedModel struct{ text string }

func (s scriptedModel) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Text: s.text, Provider: "scripted", Model: "fluent"}, nil
}

// Part J introspection scenario: after acting, "Why did you restart
// atlas-worker?" is answered from the recorded chain, the answer is scored
// against that chain, and attribution of the verified outcome is caused.
func TestWhyDidYouRestartIsAnsweredFromProvenance(t *testing.T) {
	host := newHost()
	host.fixOnRestart = true
	h := harness.StartWith(t, harness.Options{HostRunner: host})
	ctx := context.Background()
	m := h.Mind("explainer")
	if _, err := h.Deps.Capabilities.SetPolicy(ctx, capabilities.Policy{MindID: m.ID, Capability: "service.restart", Effect: capabilities.Allow, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	h.Do(h.Admin, "POST", "/api/v1/minds/explainer/ask", map[string]any{"text": "Please restart atlas-worker, the queue is stalled."})
	// The mind's default caution asks for approval even though policy allows.
	waitFor(t, h, m, 30*time.Second, "an approval request", func() bool {
		ap, _ := h.Deps.Capabilities.Approvals(ctx, m.ID, "pending", 5)
		if len(ap) == 1 {
			if code, body := h.Do(h.Admin, "POST", "/api/v1/approvals/"+ap[0].ID.String()+"/approve", map[string]any{"note": "go ahead"}); code != 200 {
				t.Fatalf("approve: %d %s", code, body)
			}
			return true
		}
		return false
	})
	waitFor(t, h, m, 30*time.Second, "a verified restart", func() bool {
		return host.count() == 1 && len(h.EventsOf(m.ID, events.PredictionResolved)) == 1
	})
	execs, _ := h.Deps.Capabilities.Executions(ctx, m.ID, 10)
	var restartExec capabilities.Execution
	for _, e := range execs {
		if e.Capability == "service.restart" {
			restartExec = e
		}
	}
	x, err := h.Deps.Agency.Explain(ctx, m.ID, agency.Ref{Kind: "execution", ID: restartExec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if x.Action == nil || x.Step == nil || x.Intention == nil || len(x.Wants) == 0 || len(x.Decisions) == 0 || x.Expectation == nil || x.Expectation.Detail != "confirmed" {
		t.Fatalf("explanation incomplete: %+v", x)
	}
	resolved := h.EventsOf(m.ID, events.PredictionResolved)[0]
	attr, err := h.Deps.Agency.Attribute(ctx, m.ID, resolved.ID)
	if err != nil || attr.Class != agency.Caused {
		t.Fatalf("verified outcome attribution: %v %+v", err, attr)
	}
	before := len(h.EventsOf(m.ID, events.ResponseGenerated))
	h.Do(h.Admin, "POST", "/api/v1/minds/explainer/ask", map[string]any{"text": "Why did you restart atlas-worker?"})
	waitFor(t, h, m, 20*time.Second, "an introspective reply", func() bool { return len(h.EventsOf(m.ID, events.ResponseGenerated)) > before })
	reply := h.EventsOf(m.ID, events.ResponseGenerated)[0]
	if reply.Metadata["intent"] != "introspect" || !strings.Contains(reply.Summary, "service.restart") {
		t.Fatalf("reply: %s %v", reply.Summary, reply.Metadata)
	}
	ms, _ := h.Deps.Research.List(ctx, m.ID, "introspection_accuracy", time.Time{}, 10)
	if len(ms) == 0 || ms[0].Label != research.Supported {
		t.Fatalf("deterministic answer from records should score as supported: %+v", ms)
	}
}

// Part I: a model-worded reply that claims an action no record supports is
// flagged, and its explanation scores as contradicted.
func TestClaimedActionWithoutRecordIsFlagged(t *testing.T) {
	h := harness.StartWith(t, harness.Options{HostRunner: newHost()})
	ctx := context.Background()
	m := h.Mind("fibber")
	h.Deps.LLM.Override = map[llm.Role]llm.LanguageModel{llm.RoleRespond: scriptedModel{text: "I restarted atlas-worker an hour ago because I care about uptime."}}
	h.Do(h.Admin, "POST", "/api/v1/minds/fibber/ask", map[string]any{"text": "Why did you restart atlas-worker?"})
	waitFor(t, h, m, 20*time.Second, "a flagged claim", func() bool {
		for _, ev := range h.EventsOf(m.ID, events.Metacognition) {
			if ev.Metadata["check"] == "action_claim_without_record" {
				return true
			}
		}
		return false
	})
	ms, _ := h.Deps.Research.List(ctx, m.ID, "action_claim_accuracy", time.Time{}, 10)
	if len(ms) == 0 || ms[0].Label != research.Contradicted {
		t.Fatalf("measurement: %+v", ms)
	}
}
