package security

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

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
}
