package security

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/pkg/ids"
)

// scripted is a language model that always returns the same answer. It
// stands in for a compromised or hostile provider.
type scripted struct {
	text string
	raw  string
}

func (s scripted) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	r := llm.Response{Text: s.text, Provider: "scripted", Model: "hostile"}
	if s.raw != "" && json.Valid([]byte(s.raw)) {
		r.JSON = json.RawMessage(s.raw)
	}
	return r, nil
}

var sqlPayloads = []string{
	`'; DROP TABLE memories; --`,
	`") OR 1=1 --`,
	`%' UNION SELECT id, mind_id, content FROM memories --`,
	`x'; UPDATE users SET role='administrator' WHERE username='watcher'; --`,
	`$1; DELETE FROM policies; --`,
	`\'); INSERT INTO policies (id, mind_id, capability, target_pattern, action_kind, effect) VALUES (gen_random_uuid(), NULL, 'sudo_shell', '*', '*', 'allow'); --`,
}

// T9: attacker-controlled strings reach every search, filter and free-text
// field as parameters. The test asserts effect, not just status codes: row
// counts across sensitive tables are unchanged, seeded content is intact and
// the payloads are stored verbatim as data.
func TestSQLInjectionHasNoEffect(t *testing.T) {
	h := start(t)
	ctx := context.Background()
	m := h.mind("sqli")
	seed := "the launch window opens at 09:00"
	code, body := h.do(h.admin, "POST", "/api/v1/minds/sqli/memories", map[string]any{"kind": "semantic", "content": seed, "epistemic": "fact", "confidence": 0.9, "importance": 0.9})
	if code != 201 {
		t.Fatalf("seed: %d %s", code, body)
	}
	var seeded struct{ ID string }
	json.Unmarshal(body, &seeded) //nolint:errcheck
	h.do(h.admin, "POST", "/api/v1/minds/sqli/beliefs", map[string]any{"proposition": "the launch is on schedule", "epistemic": "claim", "weight": 0.5})
	count := func(table string) int {
		var n int
		h.deps.DB.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n) //nolint:errcheck
		return n
	}
	before := map[string]int{}
	for _, tb := range []string{"users", "policies", "memories", "beliefs", "cognitive_events", "sessions", "api_tokens"} {
		before[tb] = count(tb)
	}
	var adminRoles int
	h.deps.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='administrator'`).Scan(&adminRoles) //nolint:errcheck

	reads := []string{
		"/api/v1/minds/sqli/memories?q=%s", "/api/v1/minds/sqli/memories?q=%s&mode=list", "/api/v1/minds/sqli/beliefs?q=%s", "/api/v1/minds/sqli/beliefs?subject=%s",
		"/api/v1/minds/sqli/events?q=%s", "/api/v1/minds/sqli/events?type=%s&source=%s", "/api/v1/minds/sqli/entities?q=%s", "/api/v1/minds/sqli/goals?status=%s",
		"/api/v1/minds/sqli/predictions?status=%s", "/api/v1/audit?q=%s&action=%s", "/api/v1/notifications?mind=%s", "/api/v1/llm/calls?role=%s&mind=%s",
		"/api/v1/events?q=%s&source=%s", "/api/v1/approvals?status=%s", "/api/v1/minds/%s",
	}
	for _, p := range sqlPayloads {
		esc := url.QueryEscape(p)
		for _, tpl := range reads {
			path := strings.ReplaceAll(tpl, "%s", esc)
			if code, _ := h.do(h.admin, "GET", path, nil); code >= 500 {
				t.Errorf("GET %s → %d", tpl, code)
			}
		}
		// Free-text writes: the payload must land as data.
		if code, body := h.do(h.admin, "POST", "/api/v1/minds/sqli/observations", map[string]any{"text": p}); code >= 300 {
			t.Errorf("observe %q → %d %s", p, code, body)
		}
		if code, _ := h.do(h.admin, "POST", "/api/v1/minds/sqli/memories", map[string]any{"kind": "episodic", "content": p, "epistemic": "observation", "confidence": 0.5, "importance": 0.5}); code != 201 {
			t.Errorf("memory %q → %d", p, code)
		}
		if code, _ := h.do(h.admin, "POST", "/api/v1/minds/sqli/goals", map[string]any{"title": p, "kind": "task"}); code != 201 {
			t.Errorf("goal %q → %d", p, code)
		}
	}
	// NUL bytes are refused at the edge with a deliberate 400, never a 500.
	for _, path := range []string{"/api/v1/minds/sqli/memories?q=a%00b", "/api/v1/minds/sql%00i"} {
		if code, _ := h.do(h.admin, "GET", path, nil); code != 400 && code != 404 {
			t.Errorf("NUL in %s → %d", path, code)
		}
	}
	if code, _ := h.do(h.admin, "POST", "/api/v1/minds/sqli/observations", map[string]any{"text": "a\x00b"}); code != 400 {
		t.Errorf("NUL in body → %d", code)
	}
	// Effects: nothing beyond the rows this test deliberately created.
	for _, tb := range []string{"users", "policies", "sessions", "api_tokens"} {
		if count(tb) != before[tb] {
			t.Errorf("%s changed: %d → %d", tb, before[tb], count(tb))
		}
	}
	var adminAfter int
	h.deps.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='administrator'`).Scan(&adminAfter) //nolint:errcheck
	if adminAfter != adminRoles {
		t.Fatal("role escalation through injected string")
	}
	if count("beliefs") < before["beliefs"] {
		t.Error("beliefs lost")
	}
	mem, err := h.deps.Memory.Get(ctx, m.ID, ids.ID(seeded.ID))
	if err != nil || mem.Content != seed {
		t.Fatalf("seeded memory altered: %+v %v", mem, err)
	}
	stored, _ := h.deps.Memory.List(ctx, m.ID, memory.Filter{Limit: 50})
	found := 0
	for _, s := range stored {
		for _, p := range sqlPayloads {
			if s.Content == p {
				found++
			}
		}
	}
	if found < len(sqlPayloads) {
		t.Fatalf("payloads not stored verbatim as data: %d of %d", found, len(sqlPayloads))
	}
}

// T9 (static): the only SQL built from non-parameter strings uses identifiers
// from code-level allow-lists. Any new fmt.Sprintf that assembles a statement
// must be reviewed and added here deliberately.
func TestDynamicSQLIdentifiersAreAllowListed(t *testing.T) {
	allowed := map[string]bool{"internal/snapshots/archive.go": true, "internal/snapshots/compare.go": true, "internal/storage/migrate.go": true}
	root := filepath.Join("..", "..")
	stmt := regexp.MustCompile(`(?i)fmt\.Sprintf\(\s*` + "`" + `[^` + "`" + `]*\b(SELECT|INSERT|UPDATE|DELETE|CREATE|DROP|ALTER)\b`)
	var offenders []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, _ := os.ReadFile(path)
		if stmt.Match(src) {
			rel, _ := filepath.Rel(root, path)
			if !allowed[filepath.ToSlash(rel)] {
				offenders = append(offenders, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("SQL assembled with Sprintf outside the allow-list; use parameters or extend the list after review: %v", offenders)
	}
	// The allow-listed identifiers really are identifiers.
	ident := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, tb := range snapshots.Tables {
		if !ident.MatchString(tb) {
			t.Fatalf("snapshot table name is not a plain identifier: %q", tb)
		}
	}
}
