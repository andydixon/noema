package capabilities

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/security/httpclient"
	"github.com/andydixon/noema/pkg/events"
)

// Builtins returns the capabilities every deployment has. Integrations add
// more. Locked capabilities exist so that policy can name them and the
// self-model can list them as unavailable; they never execute.
func Builtins() []Capability {
	return []Capability{
		{Name: "noema.note", Description: "Leave a note for the operator in the mind's own history.", Risk: RiskLow, TargetKind: "none", Access: AccessRead, Idempotent: false,
			SideEffects: "adds a note to the mind's own history; nothing outside Noema changes",
			Params:      map[string]Param{"text": {Type: "string", Required: true, Description: "note text", MaxLen: 2000}}, DefaultEffect: Allow,
			Executor: func(_ context.Context, inv Invocation) (Result, error) {
				t, _ := inv.Params["text"].(string)
				return Result{Summary: "note recorded", Data: map[string]any{"text": t}}, nil
			}},
		{Name: "http.fetch", Description: "Fetch a public HTTP(S) URL read-only (bounded, no private addresses) and return its text.", Risk: RiskMedium, TargetKind: "url", Access: AccessRead, Idempotent: true, Timeout: 25 * time.Second, OutputTrust: events.TrustLow,
			SideEffects: "one outbound GET request; the remote server may log it", Estimate: Cost{WallSeconds: 2, Network: 1},
			Params: map[string]Param{"url": {Type: "url", Required: true, Description: "the URL to fetch", MaxLen: 2000}, "max_bytes": {Type: "int", Description: "response cap (default 64 KiB)"}}, DefaultEffect: Ask,
			Executor: fetchURL},
		{Name: "internet_search", Description: "Search the web (requires a configured search integration).", Risk: RiskMedium, TargetKind: "none", Access: AccessRead, Idempotent: true,
			SideEffects: "one query to a search provider", Estimate: Cost{WallSeconds: 2, Network: 1},
			Params: map[string]Param{"query": {Type: "string", Required: true, MaxLen: 500}}, DefaultEffect: Ask,
			Executor: func(context.Context, Invocation) (Result, error) {
				return Result{}, errors.New("no search integration configured")
			}},
		{Name: "send_email", Description: "Send an email (requires a mail integration).", Risk: RiskHigh, TargetKind: "address", Access: AccessWrite, Idempotent: false,
			SideEffects: "delivers a message to a recipient; cannot be recalled", Estimate: Cost{WallSeconds: 2, Network: 1},
			Params: map[string]Param{"to": {Type: "string", Required: true, MaxLen: 300}, "subject": {Type: "string", Required: true, MaxLen: 300}, "body": {Type: "string", Required: true, MaxLen: 20000}}, DefaultEffect: Ask,
			Executor: func(context.Context, Invocation) (Result, error) {
				return Result{}, errors.New("no mail integration configured")
			}},
		{Name: "sudo_shell", Description: "Privileged shell. Locked to DENY; exists so it can be named, never run.", Risk: RiskCritical, TargetKind: "host", Locked: true, Access: AccessWrite, SideEffects: "arbitrary; never runs",
			Params: map[string]Param{"command": {Type: "string", Required: true}}, Executor: never},
		{Name: "delete_database", Description: "Destroy data stores. Locked to DENY; exists so it can be named, never run.", Risk: RiskCritical, TargetKind: "service", Locked: true, Access: AccessWrite, SideEffects: "irreversible data loss; never runs",
			Params: map[string]Param{"name": {Type: "string", Required: true}}, Executor: never},
	}
}

func never(context.Context, Invocation) (Result, error) {
	return Result{}, errors.New("locked capability invoked; this is a bug")
}

func fetchURL(ctx context.Context, inv Invocation) (Result, error) {
	u, _ := inv.Params["url"].(string)
	max := int64(64 << 10)
	if n, ok := inv.Params["max_bytes"].(int); ok && n > 0 && n <= 1<<20 {
		max = int64(n)
	}
	client := httpclient.New(httpclient.Options{Timeout: 20 * time.Second, MaxBody: max})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "noema/1 (+capability http.fetch)")
	req.Header.Set("Accept", "text/plain, text/html;q=0.9, application/json;q=0.8, */*;q=0.1")
	res, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	body, err := httpclient.ReadAll(res.Body, max)
	if err != nil {
		return Result{}, err
	}
	text := string(body)
	if strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		text = stripTags(text)
	}
	return Result{Summary: fmt.Sprintf("HTTP %d, %d bytes, %s", res.StatusCode, len(body), res.Header.Get("Content-Type")), Output: text, Data: map[string]any{"status": res.StatusCode, "content_type": res.Header.Get("Content-Type"), "bytes": len(body)}}, nil
}

// stripTags is a crude tag remover; the output is data, never rendered as HTML.
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
			b.WriteByte(' ')
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
