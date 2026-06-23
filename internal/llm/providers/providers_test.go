package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/llm"
)

func TestOpenAIWireFormat(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)                                                                                                                                                                                                             //nolint:errcheck
		w.Write([]byte("{\"model\":\"gpt-x\",\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"intent\\\":\\\"question\\\"}\\n```\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5,\"total_tokens\":17}}")) //nolint:errcheck
	}))
	defer srv.Close()
	p := NewOpenAI(Config{Name: "test", Endpoint: srv.URL + "/v1", APIKey: "sk-test-123", Model: "gpt-x", AllowPrivate: true})
	seed := int64(7)
	resp, err := p.Complete(context.Background(), llm.Request{System: "sys", Messages: []llm.Message{{Role: "user", Content: "hi"}}, JSONSchema: json.RawMessage(`{"type":"object"}`), Seed: &seed, MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test-123" || got["model"] != "gpt-x" || got["seed"] != float64(7) || got["max_tokens"] != float64(50) {
		t.Fatalf("request wrong: auth=%q body=%v", auth, got)
	}
	if msgs := got["messages"].([]any); len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("system message not prepended: %v", msgs)
	}
	if string(resp.JSON) != `{"intent":"question"}` || resp.Usage.TotalTokens != 17 || resp.Model != "gpt-x" {
		t.Fatalf("response wrong: %+v", resp)
	}
}

func TestAnthropicWireFormat(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-api-key")
		json.NewDecoder(r.Body).Decode(&got)                                                                                                                     //nolint:errcheck
		w.Write([]byte(`{"model":"claude-x","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)) //nolint:errcheck
	}))
	defer srv.Close()
	p := NewAnthropic(Config{Name: "anth", Endpoint: srv.URL, APIKey: "ak", Model: "claude-x", AllowPrivate: true})
	resp, err := p.Complete(context.Background(), llm.Request{System: "be brief", Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Text != "Hello" || resp.Usage.TotalTokens != 5 || key != "ak" || got["system"] != "be brief" {
		t.Fatalf("anthropic wrong: %v %+v %v", err, resp, got)
	}
}

func TestOllamaWireFormatAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			var got map[string]any
			json.NewDecoder(r.Body).Decode(&got) //nolint:errcheck
			if got["format"] != "json" {
				w.WriteHeader(500)
				w.Write([]byte(`{"error":"expected json format, key sk-leak"}`)) //nolint:errcheck
				return
			}
			w.Write([]byte(`{"model":"llama","message":{"role":"assistant","content":"not json at all"},"done_reason":"stop","prompt_eval_count":4,"eval_count":6}`)) //nolint:errcheck
		case "/api/embed":
			w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`)) //nolint:errcheck
		}
	}))
	defer srv.Close()
	p := NewOllama(Config{Name: "local", Endpoint: srv.URL, Model: "llama", AllowPrivate: true})
	_, err := p.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if !errors.Is(err, llm.ErrProvider) || strings.Contains(err.Error(), "sk-leak") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error should be a scrubbed provider error: %v", err)
	}
	_, err = p.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "x"}}, JSONSchema: json.RawMessage(`{}`)})
	if !errors.Is(err, llm.ErrBadJSON) {
		t.Fatalf("expected bad json, got %v", err)
	}
	e := NewOllamaEmbedder(Config{Name: "local", Endpoint: srv.URL, Model: "nomic", AllowPrivate: true}, 0)
	vecs, err := e.Embed(context.Background(), []string{"a"})
	if err != nil || len(vecs) != 1 || len(vecs[0]) != 3 || e.Dims() != 3 || e.Model() != "local/nomic" {
		t.Fatalf("embed wrong: %v %v", err, vecs)
	}
}

func TestPrivateEndpointRefusedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })) //nolint:errcheck
	defer srv.Close()
	p := NewOpenAI(Config{Name: "x", Endpoint: srv.URL, Model: "m"})
	if _, err := p.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "x"}}}); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("loopback endpoint should need allow_private: %v", err)
	}
}
