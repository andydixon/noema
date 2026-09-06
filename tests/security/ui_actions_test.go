package security

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/tests/harness"
)

// Regression: the approval centre and policy form called UI callbacks that
// were never wired, so an operator could not approve, deny or add a rule
// from the browser. Both now go through the audited server paths.
func TestOperatorDecidesInTheBrowser(t *testing.T) {
	h := harness.Start(t)
	ctx := context.Background()
	m := h.Mind("desk")
	c, csrf := h.Browser("admin", harness.AdminPass)
	res, err := c.PostForm(h.Srv.URL+"/policies", url.Values{"_csrf": {csrf}, "mind_id": {m.ID.String()}, "capability": {"noema.note"}, "effect": {"ask"}, "reason": {"notes need a look"}})
	if err != nil || res.StatusCode != 303 {
		t.Fatalf("policy form: %v %d", err, res.StatusCode)
	}
	pols, _ := h.Deps.Capabilities.Policies(ctx, m.ID)
	if len(pols) != 1 || pols[0].Effect != capabilities.Ask {
		t.Fatalf("policy not created: %+v", pols)
	}
	for _, decision := range []string{"approve", "deny"} {
		code, body := h.Do(h.Admin, "POST", "/api/v1/minds/desk/invoke", map[string]any{"capability": "noema.note", "params": map[string]any{"text": "hello"}, "reason": "test"})
		var out capabilities.Outcome
		json.Unmarshal(body, &out) //nolint:errcheck
		if code != 200 || out.Status != "pending" {
			t.Fatalf("invoke: %d %s", code, body)
		}
		res, err = c.PostForm(h.Srv.URL+"/approvals/"+out.ApprovalID.String()+"/decide", url.Values{"_csrf": {csrf}, "decision": {decision}, "note": {"from the browser"}})
		if err != nil || res.StatusCode != 303 {
			t.Fatalf("%s form: %v %d", decision, err, res.StatusCode)
		}
		a, _ := h.Deps.Capabilities.GetApproval(ctx, out.ApprovalID)
		want := map[string]string{"approve": "executed", "deny": "denied"}[decision]
		if a.Status != want || a.DecisionBy != "admin" {
			t.Fatalf("%s: status %s by %q", decision, a.Status, a.DecisionBy)
		}
	}
	// A mind's capability request is granted (narrowed) from the browser, and
	// the resulting temporary grant can be revoked there.
	ops := 20
	req, err := h.Deps.Capabilities.RequestCapability(ctx, capabilities.CapabilityRequest{MindID: m.ID, Capability: "http.fetch", TargetPattern: "https://status.example.org/*", DurationSeconds: 1800, MaxOperations: &ops, Reason: "status page needed for the incident", Risk: "none beyond one request", Fallback: "ask the operator"})
	if err != nil {
		t.Fatal(err)
	}
	res, err = c.Get(h.Srv.URL + "/approvals")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("approvals page: %v %d", err, res.StatusCode)
	}
	res, err = c.PostForm(h.Srv.URL+"/capability-requests/"+req.ID.String()+"/decide", url.Values{"_csrf": {csrf}, "decision": {"grant"}, "duration_minutes": {"10"}, "max_operations": {"3"}})
	if err != nil || res.StatusCode != 303 {
		t.Fatalf("grant form: %v %d", err, res.StatusCode)
	}
	grants, _ := h.Deps.Capabilities.ScopedGrants(ctx, m.ID, true)
	if len(grants) != 1 || *grants[0].MaxOperations != 3 || time.Until(grants[0].ExpiresAt) > 11*time.Minute || grants[0].GrantedBy != "admin" {
		t.Fatalf("grant from browser: %+v", grants)
	}
	res, err = c.PostForm(h.Srv.URL+"/scoped-grants/"+grants[0].ID.String()+"/revoke", url.Values{"_csrf": {csrf}})
	if err != nil || res.StatusCode != 303 {
		t.Fatalf("revoke form: %v %d", err, res.StatusCode)
	}
	if live, _ := h.Deps.Capabilities.ScopedGrants(ctx, m.ID, true); len(live) != 0 {
		t.Fatal("grant still live after revoke")
	}
}
