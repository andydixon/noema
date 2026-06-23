package providers

import (
	"context"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/llm"
)

// Anthropic speaks the Messages API.
type Anthropic struct{ cfg Config }

// NewAnthropic creates a client.
func NewAnthropic(cfg Config) *Anthropic { return &Anthropic{cfg: cfg} }

// Complete implements llm.LanguageModel.
func (p *Anthropic) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, map[string]string{"role": role, "content": m.Content})
	}
	system := req.System
	if len(req.JSONSchema) > 0 {
		system = strings.TrimSpace(system + "\n\nRespond with a single JSON object only, matching this JSON schema:\n" + string(req.JSONSchema))
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	body := map[string]any{"model": p.cfg.Model, "messages": msgs, "max_tokens": maxTokens}
	if system != "" {
		body["system"] = system
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}
	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if p.cfg.APIKey != "" {
		headers["x-api-key"] = p.cfg.APIKey
	}
	for k, v := range p.cfg.Headers {
		headers[k] = v
	}
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	start := time.Now()
	if err := post(ctx, p.cfg.client(), p.cfg.url("/v1/messages"), headers, body, &out); err != nil {
		return llm.Response{}, err
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	resp := llm.Response{Text: text.String(), Provider: p.cfg.Name, Model: firstNonEmpty(out.Model, p.cfg.Model), Latency: time.Since(start), FinishReason: out.StopReason,
		Usage: llm.Usage{PromptTokens: out.Usage.InputTokens, CompletionTokens: out.Usage.OutputTokens, TotalTokens: out.Usage.InputTokens + out.Usage.OutputTokens}}
	return resp, finish(req, &resp)
}
