// Package providers implements LanguageModel and Embedder clients over raw
// HTTP for OpenAI-compatible, Anthropic and Ollama endpoints. No vendor SDKs:
// the surface we need is small and this keeps the dependency tree flat.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/security/httpclient"
)

// Config is the shared provider configuration.
type Config struct {
	Name         string
	Endpoint     string
	APIKey       string // resolved plaintext; never stored on the struct beyond the request
	Model        string
	Timeout      time.Duration
	AllowPrivate bool
	Headers      map[string]string
}

func (c Config) client() *http.Client {
	t := c.Timeout
	if t <= 0 {
		t = 60 * time.Second
	}
	return httpclient.New(httpclient.Options{Timeout: t, AllowPrivate: c.AllowPrivate, MaxBody: 16 << 20})
}

func (c Config) url(path string) string {
	return strings.TrimRight(c.Endpoint, "/") + path
}

// post sends JSON and decodes JSON, mapping non-2xx to llm.ErrProvider with a
// scrubbed message (bodies may echo request content, never our key).
func post(ctx context.Context, client *http.Client, url string, headers map[string]string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "noema/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", llm.ErrProvider, scrub(err.Error()))
	}
	defer res.Body.Close()
	raw, err := httpclient.ReadAll(res.Body, 16<<20)
	if err != nil {
		return fmt.Errorf("%w: %v", llm.ErrProvider, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d: %s", llm.ErrProvider, res.StatusCode, truncate(scrub(string(raw)), 300))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: undecodable response: %v", llm.ErrProvider, err)
	}
	return nil
}

// scrub removes anything that looks like a bearer token from error text.
func scrub(s string) string {
	for _, marker := range []string{"Bearer ", "sk-", "x-api-key"} {
		if i := strings.Index(s, marker); i >= 0 {
			end := i + len(marker)
			for end < len(s) && s[end] != ' ' && s[end] != '"' && s[end] != '\n' {
				end++
			}
			s = s[:i] + marker + "[redacted]" + s[end:]
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// messages prepends the system prompt for protocols that carry it inline.
func messages(req llm.Request) []llm.Message {
	if req.System == "" {
		return req.Messages
	}
	return append([]llm.Message{{Role: "system", Content: req.System}}, req.Messages...)
}

// parseJSON extracts the first JSON object from text (models sometimes wrap
// it in prose or fences) and returns it compacted.
func parseJSON(text string) (json.RawMessage, bool) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func finish(req llm.Request, resp *llm.Response) error {
	if len(req.JSONSchema) > 0 {
		j, ok := parseJSON(resp.Text)
		if !ok {
			return llm.ErrBadJSON
		}
		resp.JSON = j
	}
	return nil
}
