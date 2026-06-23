package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) llmRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/llm/providers", require(auth.RoleObserver, s.providersList))
	m.Handle("POST /api/v1/llm/providers", require(auth.RoleAdministrator, s.providersCreate))
	m.Handle("GET /api/v1/llm/providers/{id}", require(auth.RoleObserver, s.providerGet))
	m.Handle("PUT /api/v1/llm/providers/{id}", require(auth.RoleAdministrator, s.providerPut))
	m.Handle("DELETE /api/v1/llm/providers/{id}", require(auth.RoleAdministrator, s.providerDelete))
	m.Handle("POST /api/v1/llm/providers/{id}/test", require(auth.RoleOperator, s.providerTest))
	m.Handle("GET /api/v1/llm/models", require(auth.RoleObserver, s.modelsList))
	m.Handle("POST /api/v1/llm/models", require(auth.RoleAdministrator, s.modelsCreate))
	m.Handle("PUT /api/v1/llm/models/{id}", require(auth.RoleAdministrator, s.modelPut))
	m.Handle("DELETE /api/v1/llm/models/{id}", require(auth.RoleAdministrator, s.modelDelete))
	m.Handle("GET /api/v1/llm/roles", require(auth.RoleObserver, s.rolesList))
	m.Handle("PUT /api/v1/llm/roles/{role}", require(auth.RoleAdministrator, s.rolePut))
	m.Handle("GET /api/v1/llm/calls", require(auth.RoleObserver, s.callsList))
	m.Handle("GET /api/v1/llm/calls/{id}", require(auth.RoleObserver, s.callGet))
	m.Handle("GET /api/v1/llm/usage", require(auth.RoleObserver, s.usage))
}

func llmErr(err error) error {
	if errors.Is(err, router.ErrInvalid) || errors.Is(err, secrets.ErrNoMasterKey) {
		return ValidationError(err.Error())
	}
	return err
}

// keyRef turns a typed secret into a reference: references pass through,
// anything else is encrypted under the master key (or refused without one).
func (s *Server) keyRef(typed string) (string, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" || secrets.IsReference(typed) {
		return typed, nil
	}
	ref, err := s.deps.Secrets.Encrypt(typed)
	if err != nil {
		return "", errors.New("no NOEMA_MASTER_KEY configured: set one, or enter a reference like env:OPENAI_API_KEY")
	}
	return ref, nil
}

type providerIn struct {
	Name         string            `json:"name"`
	Kind         llm.ProviderKind  `json:"kind"`
	Endpoint     string            `json:"endpoint"`
	APIKey       string            `json:"api_key"` // plaintext (encrypted) or env:/file:/enc: reference; empty keeps existing
	AllowPrivate bool              `json:"allow_private"`
	Headers      map[string]string `json:"headers"`
	TimeoutMS    int               `json:"timeout_ms"`
	Enabled      *bool             `json:"enabled"`
}

func (in providerIn) toInput(s *Server) (router.ProviderInput, error) {
	ref, err := s.keyRef(in.APIKey)
	if err != nil {
		return router.ProviderInput{}, ValidationError(err.Error())
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return router.ProviderInput{Name: in.Name, Kind: in.Kind, Endpoint: in.Endpoint, APIKeyRef: ref, AllowPrivate: in.AllowPrivate, Headers: in.Headers, Timeout: time.Duration(in.TimeoutMS) * time.Millisecond, Enabled: enabled}, nil
}

func describe(p router.Provider) router.Provider {
	p.KeyDescribed = secrets.Describe(p.APIKeyRef)
	return p
}

func (s *Server) providersList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.LLMStore.ListProviders(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]router.Provider, 0, len(list))
	for _, p := range list {
		out = append(out, describe(p))
	}
	writeJSON(w, http.StatusOK, page[router.Provider]{Items: out})
}

func (s *Server) providersCreate(w http.ResponseWriter, r *http.Request) {
	var in providerIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	pin, err := in.toInput(s)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.deps.LLMStore.CreateProvider(r.Context(), pin)
	if err != nil {
		fail(w, r, llmErr(err))
		return
	}
	s.deps.LLM.Invalidate()
	s.deps.Audit.Log(r.Context(), "llm.provider.create", "llm_provider", p.ID.String(), map[string]any{"name": p.Name, "kind": p.Kind, "endpoint": p.Endpoint})
	writeJSON(w, http.StatusCreated, describe(p))
}

func (s *Server) providerGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	p, err := s.deps.LLMStore.GetProvider(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, describe(p))
}

func (s *Server) providerPut(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in providerIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	pin, err := in.toInput(s)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.deps.LLMStore.UpdateProvider(r.Context(), id, pin)
	if err != nil {
		fail(w, r, llmErr(err))
		return
	}
	s.deps.LLM.Invalidate()
	s.deps.Audit.Log(r.Context(), "llm.provider.update", "llm_provider", p.ID.String(), map[string]any{"name": p.Name, "enabled": p.Enabled, "key_changed": in.APIKey != ""})
	writeJSON(w, http.StatusOK, describe(p))
}

func (s *Server) providerDelete(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.LLMStore.DeleteProvider(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.LLM.Invalidate()
	s.deps.Audit.Log(r.Context(), "llm.provider.delete", "llm_provider", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) providerTest(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if r.ContentLength != 0 {
		decode(r, &in) //nolint:errcheck
	}
	resp, err := s.deps.LLM.TestProvider(r.Context(), id, in.Model)
	s.deps.Audit.Log(r.Context(), "llm.provider.test", "llm_provider", id.String(), map[string]any{"ok": err == nil})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": resp.Model, "latency_ms": resp.Latency.Milliseconds(), "text": resp.Text, "usage": resp.Usage})
}

type modelIn struct {
	ProviderID     ids.ID         `json:"provider_id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	ContextWindow  int            `json:"context_window"`
	Dims           int            `json:"dims"`
	InputCostPerM  float64        `json:"input_cost_per_m"`
	OutputCostPerM float64        `json:"output_cost_per_m"`
	Params         map[string]any `json:"params"`
	Enabled        *bool          `json:"enabled"`
}

func (in modelIn) toInput() router.ModelInput {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return router.ModelInput{ProviderID: in.ProviderID, Name: in.Name, Kind: in.Kind, ContextWindow: in.ContextWindow, Dims: in.Dims, InputCostPerM: in.InputCostPerM, OutputCostPerM: in.OutputCostPerM, Params: in.Params, Enabled: enabled}
}

func (s *Server) modelsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.LLMStore.ListModels(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []router.Model{}
	}
	writeJSON(w, http.StatusOK, page[router.Model]{Items: list})
}

func (s *Server) modelsCreate(w http.ResponseWriter, r *http.Request) {
	var in modelIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	m, err := s.deps.LLMStore.CreateModel(r.Context(), in.toInput())
	if err != nil {
		fail(w, r, llmErr(err))
		return
	}
	s.deps.LLM.Invalidate()
	s.deps.Audit.Log(r.Context(), "llm.model.create", "llm_model", m.ID.String(), map[string]any{"name": m.Name, "provider": m.Provider})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) modelPut(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in modelIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	m, err := s.deps.LLMStore.UpdateModel(r.Context(), id, in.toInput())
	if err != nil {
		fail(w, r, llmErr(err))
		return
	}
	s.deps.LLM.Invalidate()
	s.deps.Audit.Log(r.Context(), "llm.model.update", "llm_model", m.ID.String(), map[string]any{"name": m.Name, "enabled": m.Enabled})
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) modelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.LLMStore.DeleteModel(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.LLM.Invalidate()
	s.deps.Audit.Log(r.Context(), "llm.model.delete", "llm_model", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rolesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.LLMStore.ListRoles(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "roles": llm.Roles})
}

func (s *Server) rolePut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ModelID     *ids.ID  `json:"model_id"`
		FallbackIDs []ids.ID `json:"fallback_ids"`
		Temperature *float64 `json:"temperature"`
		MaxTokens   *int     `json:"max_tokens"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	a := router.RoleAssignment{Role: llm.Role(r.PathValue("role")), ModelID: in.ModelID, FallbackIDs: in.FallbackIDs, Temperature: in.Temperature, MaxTokens: in.MaxTokens, UpdatedBy: actor(r)}
	if err := s.deps.LLMStore.SetRole(r.Context(), a); err != nil {
		fail(w, r, llmErr(err))
		return
	}
	s.deps.LLM.Invalidate()
	s.onLLMConfigChanged(r)
	s.deps.Audit.Log(r.Context(), "llm.role.set", "llm_role", string(a.Role), map[string]any{"model_id": in.ModelID, "fallbacks": in.FallbackIDs})
	writeJSON(w, http.StatusOK, a)
}

// onLLMConfigChanged refreshes dependents such as the memory embedder.
func (s *Server) onLLMConfigChanged(r *http.Request) {
	if s.deps.Memory != nil {
		s.deps.Memory.SetEmbedder(s.deps.LLM.Embedder(r.Context()))
	}
}

func (s *Server) callsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := router.CallFilter{Role: llm.Role(q.Get("role")), ErrorsOnly: q.Get("errors") == "1"}
	if v := q.Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			f.MindID = m.ID
		}
	}
	if v := q.Get("correlation"); v != "" {
		if id, err := ids.Parse(v); err == nil {
			f.CorrelationID = id
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	list, err := s.deps.LLMStore.ListCalls(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []router.Call{}
	}
	writeJSON(w, http.StatusOK, page[router.Call]{Items: list})
}

func (s *Server) callGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	c, err := s.deps.LLMStore.GetCall(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	window, err := time.ParseDuration(r.URL.Query().Get("window"))
	if err != nil {
		window = 24 * time.Hour
	}
	var mind ids.ID
	if v := r.URL.Query().Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			mind = m.ID
		}
	}
	rows, err := s.deps.LLMStore.Usage(r.Context(), time.Now().Add(-window), mind)
	if err != nil {
		fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []router.UsageRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"window": window.String(), "items": rows})
}
