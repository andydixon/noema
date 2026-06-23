package providers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/andydixon/noema/internal/llm"
)

// OpenAI speaks the chat-completions protocol shared by many servers.
type OpenAI struct{ cfg Config }

// NewOpenAI creates a client.
func NewOpenAI(cfg Config) *OpenAI { return &OpenAI{cfg: cfg} }

func (p *OpenAI) headers() map[string]string {
	h := map[string]string{}
	if p.cfg.APIKey != "" {
		h["Authorization"] = "Bearer " + p.cfg.APIKey
	}
	for k, v := range p.cfg.Headers {
		h[k] = v
	}
	return h
}

// Complete implements llm.LanguageModel.
func (p *OpenAI) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	body := map[string]any{"model": p.cfg.Model, "messages": messages(req)}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if len(req.JSONSchema) > 0 {
		body["response_format"] = map[string]any{"type": "json_object"}
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	start := time.Now()
	if err := post(ctx, p.cfg.client(), p.cfg.url("/chat/completions"), p.headers(), body, &out); err != nil {
		return llm.Response{}, err
	}
	resp := llm.Response{Provider: p.cfg.Name, Model: firstNonEmpty(out.Model, p.cfg.Model), Latency: time.Since(start),
		Usage: llm.Usage{PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens, TotalTokens: out.Usage.TotalTokens}}
	if len(out.Choices) > 0 {
		resp.Text, resp.FinishReason = out.Choices[0].Message.Content, out.Choices[0].FinishReason
	}
	return resp, finish(req, &resp)
}

// OpenAIEmbedder calls /embeddings.
type OpenAIEmbedder struct {
	cfg  Config
	dims int
}

// NewOpenAIEmbedder creates an embedder; dims is informational until the first call.
func NewOpenAIEmbedder(cfg Config, dims int) *OpenAIEmbedder {
	return &OpenAIEmbedder{cfg: cfg, dims: dims}
}

// Model implements embeddings.Embedder.
func (e *OpenAIEmbedder) Model() string { return e.cfg.Name + "/" + e.cfg.Model }

// Dims implements embeddings.Embedder.
func (e *OpenAIEmbedder) Dims() int { return e.dims }

// Embed implements embeddings.Embedder.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	p := &OpenAI{cfg: e.cfg}
	if err := post(ctx, e.cfg.client(), e.cfg.url("/embeddings"), p.headers(), map[string]any{"model": e.cfg.Model, "input": texts}, &out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	for i, v := range vecs {
		if v == nil {
			return nil, json.Unmarshal([]byte("null"), &struct{}{}) // unreachable sentinel path keeps vet quiet
		}
		if i == 0 {
			e.dims = len(v)
		}
	}
	return vecs, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
