package agency

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/tests/harness"
)

// Every new inspection page renders for a mind with wants, a commitment,
// an intention blocked on approval, a plan and a subordinate intention.
func TestVolitionPagesRender(t *testing.T) {
	h := harness.StartWith(t, harness.Options{HostRunner: newHost()})
	ctx := context.Background()
	m := h.Mind("pages")
	h.Do(h.Admin, "POST", "/api/v1/minds/pages/commitments", map[string]any{"description": "no production writes without approval", "kind": "operator_instruction", "polarity": "refrain", "unless": "approval", "matcher": map[string]any{"access": "write"}})
	h.Do(h.Admin, "POST", "/api/v1/minds/pages/ask", map[string]any{"text": "Please restart atlas-worker."})
	var it volition.Intention
	waitFor(t, h, m, 25*time.Second, "a blocked intention with a plan", func() bool {
		open, _ := h.Deps.Volition.Open(ctx, nil, m.ID)
		for _, x := range open {
			if x.ParentID.IsZero() && !x.PlanID.IsZero() && x.Status == volition.IntentionBlocked {
				it = x
				return true
			}
		}
		return false
	})
	c, _ := h.Browser("watcher", harness.ObserverPass)
	pages := map[string]string{
		"/minds/pages/wants":                                   "avoid unnecessary disruption",
		"/minds/pages/intentions":                              "approval required",
		"/minds/pages/intentions/" + it.ID.String():            "Formation",
		"/intentions/" + it.ID.String():                        "Formation",
		"/minds/pages/commitments":                             "no production writes without approval",
		"/minds/pages/plans":                                   "direct",
		"/minds/pages/plans/" + it.PlanID.String():             "Dependency graph",
		"/minds/pages/agency":                                  "intended",
		"/minds/pages/why?kind=intention&id=" + it.ID.String(): "Commitments",
		"/minds/pages/research":                                "does not claim",
		"/research":                                            "Operational definitions",
		"/approvals":                                           "Capability requests",
	}
	for path, want := range pages {
		res, err := c.Get(h.Srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == 303 {
			res, err = c.Get(h.Srv.URL + res.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d, missing %q", path, res.StatusCode, want)
		}
	}
}
