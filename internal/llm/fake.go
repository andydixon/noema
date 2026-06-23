package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Fake is a scripted LanguageModel for tests and demos. Responses are served
// in order; when the script is exhausted the last response repeats. A nil
// script yields a fixed "OK" answer.
type Fake struct {
	mu        sync.Mutex
	Script    []string
	Err       error
	Calls     []Request
	ModelName string
	Delay     time.Duration
}

// Complete implements LanguageModel.
func (f *Fake) Complete(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, req)
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	if f.Err != nil {
		return Response{}, f.Err
	}
	text := "OK"
	if n := len(f.Script); n > 0 {
		i := len(f.Calls) - 1
		if i >= n {
			i = n - 1
		}
		text = f.Script[i]
	}
	model := f.ModelName
	if model == "" {
		model = "fake-1"
	}
	resp := Response{Text: text, Provider: "fake", Model: model, Usage: Usage{PromptTokens: len(req.System)/4 + 10, CompletionTokens: len(text) / 4, TotalTokens: len(req.System)/4 + 10 + len(text)/4}, Latency: time.Millisecond}
	if len(req.JSONSchema) > 0 {
		if !json.Valid([]byte(text)) {
			return Response{}, ErrBadJSON
		}
		resp.JSON = json.RawMessage(text)
	}
	return resp, nil
}

// ErrFakeDown simulates an outage.
var ErrFakeDown = errors.New("fake provider down")
