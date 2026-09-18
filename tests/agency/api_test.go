package agency

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/tests/harness"
)

func TestVolitionAPI(t *testing.T) {
	h := harness.StartWith(t, harness.Options{HostRunner: newHost()})
	ctx := context.Background()
	m := h.Mind("api")
	body := map[string]any{"description": "no production writes without approval", "kind": "operator_instruction", "polarity": "refrain", "unless": "approval", "matcher": map[string]any{"access": "write", "targets": []string{"atlas-*"}}}
	if code, _ := h.Do(h.Obs, "POST", "/api/v1/minds/api/commitments", body); code != 403 {
		t.Fatalf("observer created a commitment: %d", code)
	}
	if code, out := h.Do(h.Admin, "POST", "/api/v1/minds/api/commitments", body); code != 201 {
		t.Fatalf("commitment: %d %s", code, out)
	}
	if code, out := h.Do(h.Admin, "POST", "/api/v1/minds/api/commitments", map[string]any{"description": "x", "kind": "vow", "polarity": "do"}); code != 400 {
		t.Fatalf("invalid commitment: %d %s", code, out)
	}
	h.Do(h.Admin, "POST", "/api/v1/minds/api/ask", map[string]any{"text": "Please restart atlas-worker."})
	var it volition.Intention
	waitFor(t, h, m, 25*time.Second, "an intention waiting for approval", func() bool {
		open, _ := h.Deps.Volition.Open(ctx, nil, m.ID)
		for _, x := range open {
			if x.ParentID.IsZero() && x.Status == volition.IntentionBlocked {
				it = x
				return true
			}
		}
		return false
	})
	get := func(path string, v any) {
		t.Helper()
		code, out := h.Do(h.Obs, "GET", path, nil)
		if code != 200 {
			t.Fatalf("GET %s: %d %s", path, code, out)
		}
		if err := json.Unmarshal(out, v); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
	}
	var wants struct{ Items []map[string]any }
	get("/api/v1/minds/api/wants?status=active", &wants)
	if len(wants.Items) == 0 || wants.Items[0]["effective_strength"] == nil {
		t.Fatalf("wants: %+v", wants)
	}
	var detail struct {
		Intention    volition.Intention
		Plans        []map[string]any
		Subordinates []volition.Intention
		Decisions    []volition.Decision
	}
	get("/api/v1/minds/api/intentions/"+it.ID.String(), &detail)
	if len(detail.Plans) != 1 || len(detail.Subordinates) != 1 || len(detail.Decisions) == 0 {
		t.Fatalf("intention detail: %+v", detail)
	}
	var why struct{ Points []string }
	get("/api/v1/minds/api/why?kind=intention&id="+it.ID.String(), &why)
	if !strings.Contains(strings.Join(why.Points, "\n"), "Commitment: no production writes without approval") {
		t.Fatalf("why: %v", why.Points)
	}
	var metrics struct{ Metrics map[string]any }
	get("/api/v1/minds/api/research/agency", &metrics)
	if metrics.Metrics["intentions_formed"].(float64) < 1 {
		t.Fatalf("agency metrics: %+v", metrics)
	}
	var measures struct {
		Disclaimer string
		Items      []map[string]any
	}
	get("/api/v1/research/measures", &measures)
	if !strings.Contains(measures.Disclaimer, "does not claim") || len(measures.Items) < 10 {
		t.Fatalf("measures: %+v", measures)
	}
	var timeline struct{ Items []map[string]any }
	get("/api/v1/minds/api/agency/timeline", &timeline)
	if len(timeline.Items) < 4 {
		t.Fatalf("timeline: %d", len(timeline.Items))
	}
	if code, _ := h.Do(h.Obs, "POST", "/api/v1/minds/api/intentions/"+it.ID.String()+"/abandon", map[string]any{"reason": "no"}); code != 403 {
		t.Fatalf("observer abandoned an intention: %d", code)
	}
	if code, out := h.Do(h.Admin, "POST", "/api/v1/minds/api/intentions/"+it.ID.String()+"/abandon", map[string]any{"reason": "change freeze"}); code != 200 {
		t.Fatalf("abandon: %d %s", code, out)
	}
	pending, _ := h.Deps.Capabilities.Approvals(ctx, m.ID, "pending", 10)
	if len(pending) != 0 {
		t.Fatalf("abandoned intention left %d approval request(s) open", len(pending))
	}
}
