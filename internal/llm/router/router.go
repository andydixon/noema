// Package router maps cognitive roles to configured providers and models,
// applies budgets and concurrency limits, records every call and serves
// recordings during replay (ADR-0004).
package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/andydixon/noema/internal/embeddings"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/providers"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/pkg/ids"
)

// Router resolves roles to configured models, applies budgets and
// concurrency limits, records every call and emits metrics. Cognitive code
// depends on this type only.
type Router struct {
	store   *Store
	secrets *secrets.Resolver
	log     *slog.Logger
	metrics *observability.Metrics
	pool    *scheduler.Pool

	mu       sync.RWMutex
	cache    routing
	cachedAt time.Time

	DailyTokenBudget int64                          // global hard stop (0 = unlimited)
	MindBudget       func(mind ids.ID) int64        // per-mind daily budget lookup (nil = unlimited)
	Replay           bool                           // serve recorded responses when a matching call exists
	Override         map[llm.Role]llm.LanguageModel // tests inject models per role
	now              func() time.Time
}

type routing struct {
	providers map[ids.ID]Provider
	models    map[ids.ID]Model
	roles     map[llm.Role]RoleAssignment
}

// NewRouter creates a router.
func NewRouter(store *Store, sec *secrets.Resolver, log *slog.Logger, metrics *observability.Metrics, maxConcurrent int) *Router {
	return &Router{store: store, secrets: sec, log: log, metrics: metrics, pool: scheduler.NewPool(maxConcurrent), now: time.Now}
}

// Invalidate forces the configuration cache to reload on the next call.
func (r *Router) Invalidate() {
	r.mu.Lock()
	r.cachedAt = time.Time{}
	r.mu.Unlock()
}

func (r *Router) load(ctx context.Context) (routing, error) {
	r.mu.RLock()
	if time.Since(r.cachedAt) < 30*time.Second {
		c := r.cache
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()
	provs, err := r.store.ListProviders(ctx)
	if err != nil {
		return routing{}, err
	}
	models, err := r.store.ListModels(ctx)
	if err != nil {
		return routing{}, err
	}
	roles, err := r.store.ListRoles(ctx)
	if err != nil {
		return routing{}, err
	}
	c := routing{providers: map[ids.ID]Provider{}, models: map[ids.ID]Model{}, roles: map[llm.Role]RoleAssignment{}}
	for _, p := range provs {
		c.providers[p.ID] = p
	}
	for _, m := range models {
		c.models[m.ID] = m
	}
	for _, a := range roles {
		c.roles[a.Role] = a
	}
	r.mu.Lock()
	r.cache, r.cachedAt = c, time.Now()
	r.mu.Unlock()
	return c, nil
}

// Candidates returns the ordered models (primary then fallbacks) for a role.
func (r *Router) Candidates(ctx context.Context, role llm.Role) ([]Model, error) {
	c, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	a, ok := c.roles[role]
	if !ok || a.ModelID == nil {
		return nil, llm.ErrNoModel
	}
	var out []Model
	for _, id := range append([]ids.ID{*a.ModelID}, a.FallbackIDs...) {
		m, ok := c.models[id]
		if !ok || !m.Enabled {
			continue
		}
		p, ok := c.providers[m.ProviderID]
		if !ok || !p.Enabled {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, llm.ErrNoModel
	}
	return out, nil
}

// Available reports whether a role has at least one usable model.
func (r *Router) Available(ctx context.Context, role llm.Role) bool {
	if r.Override != nil {
		if _, ok := r.Override[role]; ok {
			return true
		}
	}
	_, err := r.Candidates(ctx, role)
	return err == nil
}

// build constructs a provider client for a model, resolving the key at call time.
func (r *Router) build(c routing, m Model) (llm.LanguageModel, error) {
	p := c.providers[m.ProviderID]
	key, err := r.secrets.Resolve(p.APIKeyRef)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", p.Name, err)
	}
	cfg := providers.Config{Name: p.Name, Endpoint: p.Endpoint, APIKey: key, Model: m.Name, Timeout: p.Timeout, AllowPrivate: p.AllowPrivate, Headers: p.Headers}
	switch p.Kind {
	case llm.KindOpenAI:
		return providers.NewOpenAI(cfg), nil
	case llm.KindAnthropic:
		return providers.NewAnthropic(cfg), nil
	case llm.KindOllama:
		return providers.NewOllama(cfg), nil
	}
	return nil, fmt.Errorf("provider %s: unsupported kind %s", p.Name, p.Kind)
}

// Embedder returns the embedder configured for llm.RoleEmbed, or embeddings.None.
func (r *Router) Embedder(ctx context.Context) embeddings.Embedder {
	c, err := r.load(ctx)
	if err != nil {
		return embeddings.None{}
	}
	a, ok := c.roles[llm.RoleEmbed]
	if !ok || a.ModelID == nil {
		return embeddings.None{}
	}
	m, ok := c.models[*a.ModelID]
	if !ok || !m.Enabled {
		return embeddings.None{}
	}
	p, ok := c.providers[m.ProviderID]
	if !ok || !p.Enabled {
		return embeddings.None{}
	}
	key, err := r.secrets.Resolve(p.APIKeyRef)
	if err != nil {
		return embeddings.None{}
	}
	cfg := providers.Config{Name: p.Name, Endpoint: p.Endpoint, APIKey: key, Model: m.Name, Timeout: p.Timeout, AllowPrivate: p.AllowPrivate, Headers: p.Headers}
	switch p.Kind {
	case llm.KindOllama:
		return providers.NewOllamaEmbedder(cfg, m.Dims)
	case llm.KindOpenAI:
		return providers.NewOpenAIEmbedder(cfg, m.Dims)
	}
	return embeddings.None{}
}

// Hash returns the deterministic request hash used for replay matching. It
// covers everything that influences the model's answer.
func Hash(model string, req llm.Request) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	enc.Encode(struct { //nolint:errcheck
		Model string
		Req   llm.Request
	}{model, req})
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Complete routes a request for a role: budget check, primary then fallbacks,
// recording and metrics. The returned response carries the call id so events
// can cite it.
func (r *Router) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Role == "" {
		return llm.Response{}, fmt.Errorf("%w: role required", ErrInvalid)
	}
	if r.Override != nil {
		if lm, ok := r.Override[req.Role]; ok {
			return r.call(ctx, lm, "override", "override", 0, 0, req)
		}
	}
	c, err := r.load(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	cands, err := r.Candidates(ctx, req.Role)
	if err != nil {
		return llm.Response{}, err
	}
	if err := r.checkBudget(ctx, req.MindID); err != nil {
		return llm.Response{}, err
	}
	var lastErr error
	for i, m := range cands {
		lm, err := r.build(c, m)
		if err != nil {
			lastErr = err
			continue
		}
		applyDefaults(&req, c.roles[req.Role], m)
		resp, err := r.call(ctx, lm, m.Provider, m.Name, m.InputCostPerM, m.OutputCostPerM, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrBudgetExceeded) {
			break
		}
		if i < len(cands)-1 {
			r.log.Warn("model failed; trying fallback", "role", req.Role, "model", m.Name, "err", err)
		}
	}
	return llm.Response{}, lastErr
}

func applyDefaults(req *llm.Request, a RoleAssignment, m Model) {
	if req.Temperature == nil {
		if a.Temperature != nil {
			req.Temperature = a.Temperature
		} else if t, ok := m.Params["temperature"].(float64); ok {
			req.Temperature = &t
		}
	}
	if req.MaxTokens == 0 {
		if a.MaxTokens != nil {
			req.MaxTokens = *a.MaxTokens
		} else if t, ok := m.Params["max_tokens"].(float64); ok {
			req.MaxTokens = int(t)
		}
	}
}

func (r *Router) checkBudget(ctx context.Context, mind ids.ID) error {
	dayStart := r.now().Truncate(24 * time.Hour)
	if r.DailyTokenBudget > 0 {
		used, err := r.store.TokensSince(ctx, dayStart, ids.Nil)
		if err != nil {
			return err
		}
		if used >= r.DailyTokenBudget {
			return fmt.Errorf("%w: global daily budget %d reached", llm.ErrBudgetExceeded, r.DailyTokenBudget)
		}
	}
	if r.MindBudget != nil && !mind.IsZero() {
		if b := r.MindBudget(mind); b > 0 {
			used, err := r.store.TokensSince(ctx, dayStart, mind)
			if err != nil {
				return err
			}
			if used >= b {
				return fmt.Errorf("%w: mind daily budget %d reached", llm.ErrBudgetExceeded, b)
			}
		}
	}
	return nil
}

func (r *Router) call(ctx context.Context, lm llm.LanguageModel, provider, model string, inCost, outCost float64, req llm.Request) (llm.Response, error) {
	hash := Hash(model, req)
	reqJSON, _ := json.Marshal(req)
	if r.Replay {
		if rec, err := r.store.FindRecorded(ctx, hash); err == nil && len(rec.Response) > 0 {
			var resp llm.Response
			if json.Unmarshal(rec.Response, &resp) == nil {
				resp.Replayed, resp.CallID = true, ids.New()
				r.store.RecordCall(ctx, Call{ID: resp.CallID, MindID: req.MindID, CorrelationID: req.CorrelationID, Role: req.Role, Purpose: req.Purpose, Provider: provider, Model: model, //nolint:errcheck
					PromptName: req.PromptName, PromptVersion: req.PromptVersion, RequestHash: hash, Request: reqJSON, Response: rec.Response, Replayed: true, Seed: req.Seed})
				return resp, nil
			}
		}
	}
	var resp llm.Response
	var callErr error
	start := time.Now()
	err := r.pool.Run(ctx, func(ctx context.Context) error {
		cctx := ctx
		if req.Timeout > 0 {
			var cancel context.CancelFunc
			cctx, cancel = context.WithTimeout(ctx, req.Timeout)
			defer cancel()
		}
		resp, callErr = lm.Complete(cctx, req)
		return nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	latency := time.Since(start)
	call := Call{ID: ids.New(), MindID: req.MindID, CorrelationID: req.CorrelationID, Role: req.Role, Purpose: req.Purpose, Provider: provider, Model: model,
		PromptName: req.PromptName, PromptVersion: req.PromptVersion, RequestHash: hash, Request: reqJSON, LatencyMS: int(latency.Milliseconds()), Seed: req.Seed}
	if r.metrics != nil {
		r.metrics.LLMRequests.WithLabelValues(provider, string(req.Role)).Inc()
	}
	if callErr != nil {
		call.Error = callErr.Error()
		if r.metrics != nil {
			r.metrics.LLMFailures.WithLabelValues(provider).Inc()
		}
		r.store.RecordCall(ctx, call) //nolint:errcheck
		return llm.Response{}, callErr
	}
	resp.CallID, resp.Latency = call.ID, latency
	resp.Cost = float64(resp.Usage.PromptTokens)*inCost/1e6 + float64(resp.Usage.CompletionTokens)*outCost/1e6
	call.PromptTokens, call.OutputTokens, call.Cost = resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Cost
	call.Response, _ = json.Marshal(resp)
	if r.metrics != nil {
		r.metrics.LLMTokens.WithLabelValues(provider, "prompt").Add(float64(resp.Usage.PromptTokens))
		r.metrics.LLMTokens.WithLabelValues(provider, "completion").Add(float64(resp.Usage.CompletionTokens))
	}
	if err := r.store.RecordCall(ctx, call); err != nil {
		r.log.Error("record llm call failed", "err", err)
	}
	return resp, nil
}

// TestProvider performs a tiny completion against the first enabled chat model
// of a provider (or the given model) and records the outcome. It never returns
// secret material.
func (r *Router) TestProvider(ctx context.Context, providerID ids.ID, modelName string) (llm.Response, error) {
	c, err := r.load(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	p, ok := c.providers[providerID]
	if !ok {
		return llm.Response{}, fmt.Errorf("%w: provider not found", ErrInvalid)
	}
	var m Model
	for _, cand := range c.models {
		if cand.ProviderID == providerID && cand.Kind == "chat" && (modelName == "" || cand.Name == modelName) {
			m = cand
			break
		}
	}
	if m.ID.IsZero() {
		m = Model{ProviderID: providerID, Provider: p.Name, Name: modelName}
	}
	if m.Name == "" {
		return llm.Response{}, fmt.Errorf("%w: add a chat model to test this provider", ErrInvalid)
	}
	lm, err := r.build(c, m)
	if err != nil {
		r.store.RecordProviderTest(ctx, providerID, false, err.Error()) //nolint:errcheck
		return llm.Response{}, err
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := r.call(tctx, lm, p.Name, m.Name, m.InputCostPerM, m.OutputCostPerM, llm.Request{Role: llm.RoleJudge, Purpose: "connectivity test", MaxTokens: 8, Messages: []llm.Message{{Role: "user", Content: "Reply with the single word OK."}}})
	if err != nil {
		r.store.RecordProviderTest(ctx, providerID, false, err.Error()) //nolint:errcheck
		return llm.Response{}, err
	}
	r.store.RecordProviderTest(ctx, providerID, true, fmt.Sprintf("%s answered in %dms", resp.Model, resp.Latency.Milliseconds())) //nolint:errcheck
	r.Invalidate()
	return resp, nil
}
