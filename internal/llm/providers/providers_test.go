package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestSigV4MatchesReferenceSigner pins the signer to signatures computed by
// botocore for the same request, with and without a session token.
func TestSigV4MatchesReferenceSigner(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	at := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	mk := func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.eu-west-2.amazonaws.com/model/"+awsPathSegment("anthropic.claude-3-haiku-20240307-v1:0")+"/converse", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	req := mk()
	signV4(req, body, awsCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, "eu-west-2", "bedrock", at)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260915/eu-west-2/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=91ab2cb0d4ac1c8ff730487209ce46355c4d033b4d693361ca422142f0f66c9f"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("signature\n got %s\nwant %s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20260915T100000Z" {
		t.Fatal(req.Header.Get("X-Amz-Date"))
	}
	req = mk()
	signV4(req, body, awsCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "SESSIONTOKENEXAMPLE"}, "eu-west-2", "bedrock", at)
	want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260915/eu-west-2/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=72345f5e4ff70084eb6c87e22a2b47c7234de67f71fe2faca1e7840a74c66a2b"
	if got := req.Header.Get("Authorization"); got != want || req.Header.Get("X-Amz-Security-Token") != "SESSIONTOKENEXAMPLE" {
		t.Fatalf("session signature\n got %s\nwant %s", got, want)
	}
	if c, ok := parseAWSCredentials("AKID:secret:tok"); !ok || c.SessionToken != "tok" {
		t.Fatal("credential parsing")
	}
	if _, ok := parseAWSCredentials("just-a-key"); ok {
		t.Fatal("a bare key must be rejected")
	}
}

// TestBedrockWireFormat: Converse request shape, signed headers, response
// parsing, JSON-mode instruction, and embeddings for Titan and Cohere.
func TestBedrockWireFormat(t *testing.T) {
	var got map[string]any
	var auth, date, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, date, path = r.Header.Get("Authorization"), r.Header.Get("X-Amz-Date"), r.URL.RawPath
		if path == "" {
			path = r.URL.Path
		}
		json.NewDecoder(r.Body).Decode(&got) //nolint:errcheck
		switch {
		case strings.HasSuffix(r.URL.Path, "/converse"):
			w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"{\"intent\":\"statement\"}"}]}},"stopReason":"end_turn","usage":{"inputTokens":7,"outputTokens":3,"totalTokens":10}}`)) //nolint:errcheck
		case strings.Contains(r.URL.Path, "cohere"):
			w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`)) //nolint:errcheck
		default:
			w.Write([]byte(`{"embedding":[0.5,0.6]}`)) //nolint:errcheck
		}
	}))
	defer srv.Close()
	if _, err := NewBedrock(Config{Name: "b", Endpoint: "https://bedrock-runtime.eu-west-2.amazonaws.com", APIKey: "nokey", Model: "m"}); err == nil {
		t.Fatal("malformed credential accepted")
	}
	if _, err := NewBedrock(Config{Name: "b", Endpoint: "https://example.com", APIKey: "a:b", Model: "m"}); err == nil {
		t.Fatal("non-Bedrock endpoint accepted")
	}
	p, err := NewBedrock(Config{Name: "b", Endpoint: srv.URL, APIKey: "AKID:SECRET:TOKEN", Model: "anthropic.claude-3-haiku-20240307-v1:0", AllowPrivate: true, Headers: map[string]string{"X-Noema-Region": "eu-west-2"}})
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC) }
	resp, err := p.Complete(context.Background(), llm.Request{System: "be brief", Messages: []llm.Message{{Role: "user", Content: "hi"}}, MaxTokens: 50, JSONSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil || resp.Text == "" || resp.Usage.TotalTokens != 10 || string(resp.JSON) != `{"intent":"statement"}` || resp.FinishReason != "end_turn" {
		t.Fatalf("converse: %v %+v", err, resp)
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKID/20260915/eu-west-2/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=") || date != "20260915T100000Z" {
		t.Fatalf("signing headers: %q %q", auth, date)
	}
	if path != "/model/anthropic.claude-3-haiku-20240307-v1%3A0/converse" {
		t.Fatalf("model id must be encoded once in the path: %s", path)
	}
	sys := got["system"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(sys, "be brief") || !strings.Contains(sys, "single JSON object") || got["inferenceConfig"].(map[string]any)["maxTokens"] != float64(50) {
		t.Fatalf("request body: %+v", got)
	}
	if strings.Contains(fmt.Sprint(got), "SECRET") {
		t.Fatal("secret leaked into the body")
	}
	titan, _ := NewBedrockEmbedder(Config{Name: "b", Endpoint: srv.URL, APIKey: "AKID:SECRET", Model: "amazon.titan-embed-text-v2:0", AllowPrivate: true}, 2)
	if v, err := titan.Embed(context.Background(), []string{"a", "b"}); err != nil || len(v) != 2 || v[1][0] != 0.5 || titan.Model() != "bedrock/amazon.titan-embed-text-v2:0" {
		t.Fatalf("titan: %v %v", err, v)
	}
	cohere, _ := NewBedrockEmbedder(Config{Name: "b", Endpoint: srv.URL, APIKey: "AKID:SECRET", Model: "cohere.embed-english-v3", AllowPrivate: true}, 2)
	if v, err := cohere.Embed(context.Background(), []string{"a", "b"}); err != nil || len(v) != 2 || v[1][1] != 0.4 || got["input_type"] != "search_document" {
		t.Fatalf("cohere: %v %v", err, v)
	}
}
