package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/integrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) integrationRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/integrations", require(auth.RoleObserver, s.integrationsList))
	m.Handle("POST /api/v1/integrations", require(auth.RoleAdministrator, s.integrationsCreate))
	m.Handle("GET /api/v1/integrations/{id}", require(auth.RoleObserver, s.integrationGet))
	m.Handle("PUT /api/v1/integrations/{id}", require(auth.RoleAdministrator, s.integrationPut))
	m.Handle("DELETE /api/v1/integrations/{id}", require(auth.RoleAdministrator, s.integrationDelete))
	m.Handle("POST /api/v1/integrations/{id}/run", require(auth.RoleOperator, s.integrationRun))
	m.Handle("GET /api/v1/integrations/{id}/runs", require(auth.RoleObserver, s.integrationRuns))
	m.Handle("GET /api/v1/integrations/{id}/documents", require(auth.RoleObserver, s.integrationDocuments))
	// Webhook delivery: authenticated by signature, not by session.
	m.HandleFunc("POST /api/v1/webhooks/{id}", s.webhookReceive)
}

func integErr(err error) error {
	if errors.Is(err, integrations.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

type integrationIn struct {
	MindID  string            `json:"mind"` // slug or id
	Kind    integrations.Kind `json:"kind"`
	Name    string            `json:"name"`
	Config  map[string]any    `json:"config"`
	Secret  string            `json:"secret"` // plaintext (encrypted) or env:/file:/enc:; empty keeps existing
	Trust   events.Trust      `json:"trust"`
	Enabled *bool             `json:"enabled"`
}

func (s *Server) integrationInput(r *http.Request, in integrationIn) (integrations.Input, error) {
	var mind ids.ID
	if in.MindID != "" {
		m, err := s.deps.Minds.Resolve(r.Context(), in.MindID)
		if err != nil {
			return integrations.Input{}, err
		}
		mind = m.ID
	}
	ref, err := s.keyRef(in.Secret)
	if err != nil {
		return integrations.Input{}, ValidationError(err.Error())
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return integrations.Input{MindID: mind, Kind: in.Kind, Name: in.Name, Config: in.Config, SecretRef: ref, Trust: in.Trust, Enabled: enabled}, nil
}

func (s *Server) integrationsList(w http.ResponseWriter, r *http.Request) {
	var mind ids.ID
	if v := r.URL.Query().Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			mind = m.ID
		}
	}
	list, err := s.deps.Integrations.Store.List(r.Context(), mind)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []integrations.Integration{}
	}
	writeJSON(w, http.StatusOK, page[integrations.Integration]{Items: list})
}

func (s *Server) integrationsCreate(w http.ResponseWriter, r *http.Request) {
	var in integrationIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	input, err := s.integrationInput(r, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	i, err := s.deps.Integrations.Store.Create(r.Context(), input)
	if err != nil {
		fail(w, r, integErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "integration.create", "integration", i.ID.String(), map[string]any{"kind": i.Kind, "name": i.Name, "mind": i.MindID})
	s.deps.Events.OperatorAction(r.Context(), i.MindID, actor(r), "integration.create", "Operator "+actor(r)+" connected "+string(i.Kind)+" integration "+i.Name, map[string]any{"integration_id": i.ID})
	writeJSON(w, http.StatusCreated, i)
}

func (s *Server) integration(w http.ResponseWriter, r *http.Request) (integrations.Integration, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return integrations.Integration{}, false
	}
	i, err := s.deps.Integrations.Store.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return integrations.Integration{}, false
	}
	return i, true
}

func (s *Server) integrationGet(w http.ResponseWriter, r *http.Request) {
	i, ok := s.integration(w, r)
	if !ok {
		return
	}
	runs, _ := s.deps.Integrations.Store.Runs(r.Context(), i.ID, 10)
	docs, _ := s.deps.Integrations.Store.Documents(r.Context(), i.ID, 20)
	writeJSON(w, http.StatusOK, map[string]any{"integration": i, "runs": runs, "documents": docs})
}

func (s *Server) integrationPut(w http.ResponseWriter, r *http.Request) {
	i, ok := s.integration(w, r)
	if !ok {
		return
	}
	var in integrationIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	input, err := s.integrationInput(r, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.deps.Integrations.Store.Update(r.Context(), i.ID, input)
	if err != nil {
		fail(w, r, integErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "integration.update", "integration", i.ID.String(), map[string]any{"enabled": out.Enabled, "secret_changed": in.Secret != ""})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) integrationDelete(w http.ResponseWriter, r *http.Request) {
	i, ok := s.integration(w, r)
	if !ok {
		return
	}
	if err := s.deps.Integrations.Store.Delete(r.Context(), i.ID); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "integration.delete", "integration", i.ID.String(), map[string]any{"name": i.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) integrationRun(w http.ResponseWriter, r *http.Request) {
	i, ok := s.integration(w, r)
	if !ok {
		return
	}
	run, err := s.deps.Integrations.RunOne(r.Context(), i.ID)
	s.deps.Audit.Log(r.Context(), "integration.run", "integration", i.ID.String(), map[string]any{"observations": run.Observations, "error": run.Error})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"run": run, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) integrationRuns(w http.ResponseWriter, r *http.Request) {
	i, ok := s.integration(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.deps.Integrations.Store.Runs(r.Context(), i.ID, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if runs == nil {
		runs = []integrations.Run{}
	}
	writeJSON(w, http.StatusOK, page[integrations.Run]{Items: runs})
}

func (s *Server) integrationDocuments(w http.ResponseWriter, r *http.Request) {
	i, ok := s.integration(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	docs, err := s.deps.Integrations.Store.Documents(r.Context(), i.ID, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if docs == nil {
		docs = []integrations.Document{}
	}
	writeJSON(w, http.StatusOK, page[integrations.Document]{Items: docs})
}

// webhookReceive verifies the HMAC signature and timestamp, rejects replays,
// and ingests the body as an observation. Unsigned integrations (no secret)
// are refused outright.
func (s *Server) webhookReceive(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
		return
	}
	i, err := s.deps.Integrations.Store.Get(r.Context(), id)
	if err != nil || i.Kind != integrations.Webhook || !i.Enabled {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
		return
	}
	if !s.webhookLimiter.Allow(id.String()) {
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "slow down")
		return
	}
	if i.SecretRef == "" {
		writeError(w, r, http.StatusForbidden, "unsigned", "this webhook has no secret configured; unsigned deliveries are refused")
		return
	}
	secret, err := s.deps.Secrets.Resolve(i.SecretRef)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "secret", "webhook secret unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10+1))
	if err != nil || len(body) > 256<<10 {
		writeError(w, r, http.StatusRequestEntityTooLarge, "too_large", "body over 256 KiB")
		return
	}
	sig := r.Header.Get("X-Noema-Signature")
	if err := integrations.Verify(secret, r.Header.Get("X-Noema-Timestamp"), sig, body, time.Now()); err != nil {
		s.deps.Audit.Log(r.Context(), "webhook.rejected", "integration", id.String(), map[string]any{"reason": err.Error()})
		writeError(w, r, http.StatusUnauthorized, "signature", err.Error())
		return
	}
	if s.replays.Check(sig, time.Now()) {
		writeError(w, r, http.StatusConflict, "replay", integrations.ErrReplay.Error())
		return
	}
	ev, err := s.deps.Integrations.Ingest(r.Context(), i, body)
	if err != nil {
		fail(w, r, integErr(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"event_id": ev.ID})
}
