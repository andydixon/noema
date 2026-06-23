package providers

import (
	"context"
	"time"

	"github.com/andydixon/noema/internal/llm"
)

// Ollama speaks the native Ollama API, which exposes seeds and JSON mode
// directly; llama.cpp servers should use the OpenAI kind instead.
type Ollama struct{ cfg Config }

// NewOllama creates a client.
func NewOllama(cfg Config) *Ollama { return &Ollama{cfg: cfg} }

// Complete implements llm.LanguageModel.
func (p *Ollama) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	opts := map[string]any{}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if req.Seed != nil {
		opts["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	body := map[string]any{"model": p.cfg.Model, "messages": messages(req), "stream": false, "options": opts}
	if len(req.JSONSchema) > 0 {
		body["format"] = "json"
	}
	var out struct {
		Model           string                   `json:"model"`
		Message         struct{ Content string } `json:"message"`
		DoneReason      string                   `json:"done_reason"`
		PromptEvalCount int                      `json:"prompt_eval_count"`
		EvalCount       int                      `json:"eval_count"`
	}
	start := time.Now()
	if err := post(ctx, p.cfg.client(), p.cfg.url("/api/chat"), p.cfg.Headers, body, &out); err != nil {
		return llm.Response{}, err
	}
	resp := llm.Response{Text: out.Message.Content, Provider: p.cfg.Name, Model: firstNonEmpty(out.Model, p.cfg.Model), Latency: time.Since(start), FinishReason: out.DoneReason,
		Usage: llm.Usage{PromptTokens: out.PromptEvalCount, CompletionTokens: out.EvalCount, TotalTokens: out.PromptEvalCount + out.EvalCount}}
	return resp, finish(req, &resp)
}

// OllamaEmbedder calls /api/embed.
type OllamaEmbedder struct {
	cfg  Config
	dims int
}

// NewOllamaEmbedder creates an embedder.
func NewOllamaEmbedder(cfg Config, dims int) *OllamaEmbedder {
	return &OllamaEmbedder{cfg: cfg, dims: dims}
}

// Model implements embeddings.Embedder.
func (e *OllamaEmbedder) Model() string { return e.cfg.Name + "/" + e.cfg.Model }

// Dims implements embeddings.Embedder.
func (e *OllamaEmbedder) Dims() int { return e.dims }

// Embed implements embeddings.Embedder.
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := post(ctx, e.cfg.client(), e.cfg.url("/api/embed"), e.cfg.Headers, map[string]any{"model": e.cfg.Model, "input": texts}, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) > 0 {
		e.dims = len(out.Embeddings[0])
	}
	return out.Embeddings, nil
}
