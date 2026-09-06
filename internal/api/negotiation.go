package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/pkg/ids"
)

// negotiationRoutes: structured capability requests from minds and the
// scoped, expiring grants operators make in answer (or directly).
func (s *Server) negotiationRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/capability-requests", require(auth.RoleObserver, s.capabilityRequestsList))
	m.Handle("GET /api/v1/capability-requests/{id}", require(auth.RoleObserver, s.capabilityRequestGet))
	m.Handle("POST /api/v1/capability-requests/{id}/grant", require(auth.RoleOperator, s.capabilityRequestGrant))
	m.Handle("POST /api/v1/capability-requests/{id}/deny", require(auth.RoleOperator, s.capabilityRequestDeny))
	m.Handle("GET /api/v1/scoped-grants", require(auth.RoleObserver, s.scopedGrantsList))
	m.Handle("POST /api/v1/minds/{mind}/scoped-grants", require(auth.RoleOperator, s.scopedGrantCreate))
	m.Handle("DELETE /api/v1/scoped-grants/{id}", require(auth.RoleOperator, s.scopedGrantRevoke))
}

func negotiationErr(err error) error {
	if errors.Is(err, capabilities.ErrAttentionBudget) || errors.Is(err, capabilities.ErrNotRequestable) || errors.Is(err, capabilities.ErrReservedDecider) {
		return ValidationError(err.Error())
	}
	return capErr(err)
}

func (s *Server) mindFilter(r *http.Request) ids.ID {
	if v := r.URL.Query().Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			return m.ID
		}
	}
	return ids.Nil
}

func (s *Server) capabilityRequestsList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.deps.Capabilities.CapabilityRequests(r.Context(), s.mindFilter(r), r.URL.Query().Get("status"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []capabilities.CapabilityRequest{}
	}
	writeJSON(w, http.StatusOK, page[capabilities.CapabilityRequest]{Items: list})
}

func (s *Server) capabilityRequestGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	req, err := s.deps.Capabilities.GetCapabilityRequest(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// DecideCapabilityRequest grants or denies a request as the signed-in
// operator; shared with the UI so both surfaces audit the same way.
func (s *Server) DecideCapabilityRequest(r *http.Request, id ids.ID, grant bool, note string, n capabilities.Narrowing) (capabilities.CapabilityRequest, error) {
	out, err := s.deps.Capabilities.DecideCapabilityRequest(r.Context(), id, actor(r), grant, note, n)
	if err != nil {
		return out, negotiationErr(err)
	}
	action := "capability_request.deny"
	if grant {
		action = "capability_request.grant"
	}
	s.deps.Audit.Log(r.Context(), action, "capability_request", id.String(), map[string]any{"mind": out.MindID, "capability": out.Capability, "grant_id": out.GrantID, "note": note, "duration_seconds": n.DurationSeconds, "max_operations": n.MaxOperations, "target": n.TargetPattern})
	return out, nil
}

func (s *Server) capabilityRequestGrant(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		Note            string `json:"note"`
		DurationSeconds int    `json:"duration_seconds"`
		MaxOperations   *int   `json:"max_operations"`
		TargetPattern   string `json:"target_pattern"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, r, err)
			return
		}
	}
	out, err := s.DecideCapabilityRequest(r, id, true, in.Note, capabilities.Narrowing{DurationSeconds: in.DurationSeconds, MaxOperations: in.MaxOperations, TargetPattern: in.TargetPattern})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) capabilityRequestDeny(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, r, err)
			return
		}
	}
	out, err := s.DecideCapabilityRequest(r, id, false, in.Note, capabilities.Narrowing{})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) scopedGrantsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Capabilities.ScopedGrants(r.Context(), s.mindFilter(r), r.URL.Query().Get("live") != "false")
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []capabilities.ScopedGrant{}
	}
	writeJSON(w, http.StatusOK, page[capabilities.ScopedGrant]{Items: list})
}

// CreateScopedGrant grants temporary authority directly; shared with the UI.
func (s *Server) CreateScopedGrant(r *http.Request, in capabilities.GrantInput) (capabilities.ScopedGrant, error) {
	in.By = actor(r)
	g, err := s.deps.Capabilities.Grant(r.Context(), in)
	if err != nil {
		return g, negotiationErr(err)
	}
	s.deps.Audit.Log(r.Context(), "scoped_grant.create", "scoped_grant", g.ID.String(), map[string]any{"mind": g.MindID, "capability": g.Capability, "target": g.TargetPattern, "access": g.Access, "expires_at": g.ExpiresAt, "max_operations": g.MaxOperations, "param_constraints": g.ParamConstraints})
	return g, nil
}

func (s *Server) scopedGrantCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Capability       string            `json:"capability"`
		TargetPattern    string            `json:"target_pattern"`
		ParamConstraints map[string]string `json:"param_constraints"`
		Access           string            `json:"access"`
		DurationSeconds  int               `json:"duration_seconds"`
		MaxOperations    *int              `json:"max_operations"`
		Note             string            `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	g, err := s.CreateScopedGrant(r, capabilities.GrantInput{MindID: m.ID, Capability: in.Capability, TargetPattern: in.TargetPattern, ParamConstraints: in.ParamConstraints, Access: capabilities.Access(in.Access),
		Duration: time.Duration(in.DurationSeconds) * time.Second, MaxOperations: in.MaxOperations, Note: in.Note})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) scopedGrantRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.Capabilities.RevokeScopedGrant(r.Context(), id, actor(r)); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "scoped_grant.revoke", "scoped_grant", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}
