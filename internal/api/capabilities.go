package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) capabilityRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/capabilities", require(auth.RoleObserver, s.capabilitiesList))
	m.Handle("GET /api/v1/policies", require(auth.RoleObserver, s.policiesList))
	m.Handle("POST /api/v1/policies", require(auth.RoleAdministrator, s.policiesCreate))
	m.Handle("DELETE /api/v1/policies/{id}", require(auth.RoleAdministrator, s.policyDelete))
	m.Handle("GET /api/v1/minds/{mind}/capabilities", require(auth.RoleObserver, s.mindCapabilities))
	m.Handle("POST /api/v1/minds/{mind}/invoke", require(auth.RoleOperator, s.invoke))
	m.Handle("GET /api/v1/approvals", require(auth.RoleObserver, s.approvalsList))
	m.Handle("GET /api/v1/approvals/{id}", require(auth.RoleObserver, s.approvalGet))
	m.Handle("POST /api/v1/approvals/{id}/approve", require(auth.RoleOperator, s.approvalApprove))
	m.Handle("POST /api/v1/approvals/{id}/deny", require(auth.RoleOperator, s.approvalDeny))
	m.Handle("GET /api/v1/grants", require(auth.RoleObserver, s.grantsList))
	m.Handle("DELETE /api/v1/grants/{id}", require(auth.RoleOperator, s.grantRevoke))
	m.Handle("GET /api/v1/executions", require(auth.RoleObserver, s.executionsList))
}

func capErr(err error) error {
	if errors.Is(err, capabilities.ErrInvalid) || errors.Is(err, capabilities.ErrUnknown) || errors.Is(err, capabilities.ErrForbiddenContext) {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) capabilitiesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, page[capabilities.Capability]{Items: s.deps.Capabilities.Registry.List()})
}

func (s *Server) policiesList(w http.ResponseWriter, r *http.Request) {
	var mind ids.ID
	if v := r.URL.Query().Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			mind = m.ID
		}
	}
	list, err := s.deps.Capabilities.Policies(r.Context(), mind)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []capabilities.Policy{}
	}
	writeJSON(w, http.StatusOK, page[capabilities.Policy]{Items: list})
}

// CreatePolicy validates and stores a rule; shared with the UI.
func (s *Server) CreatePolicy(r *http.Request, p capabilities.Policy) (capabilities.Policy, error) {
	p.CreatedBy = actor(r)
	out, err := s.deps.Capabilities.SetPolicy(r.Context(), p)
	if err != nil {
		return out, capErr(err)
	}
	s.deps.Audit.Log(r.Context(), "policy.create", "policy", out.ID.String(), map[string]any{"mind": out.MindID, "capability": out.Capability, "target": out.TargetPattern, "effect": out.Effect})
	if !out.MindID.IsZero() {
		s.deps.Events.OperatorAction(r.Context(), out.MindID, actor(r), "policy.create", "Operator "+actor(r)+" set "+string(out.Effect)+" for "+out.Capability+" on "+out.TargetPattern, map[string]any{"policy_id": out.ID})
	}
	return out, nil
}

func (s *Server) policiesCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MindID        ids.ID              `json:"mind_id"`
		Capability    string              `json:"capability"`
		TargetPattern string              `json:"target_pattern"`
		ActionKind    string              `json:"action_kind"`
		Effect        capabilities.Effect `json:"effect"`
		HoursFrom     *int                `json:"hours_from"`
		HoursTo       *int                `json:"hours_to"`
		Reason        string              `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.CreatePolicy(r, capabilities.Policy{MindID: in.MindID, Capability: in.Capability, TargetPattern: in.TargetPattern, ActionKind: in.ActionKind, Effect: in.Effect, HoursFrom: in.HoursFrom, HoursTo: in.HoursTo, Reason: in.Reason})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) policyDelete(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.Capabilities.DeletePolicy(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "policy.delete", "policy", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) mindCapabilities(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	inv, err := s.deps.Capabilities.Inventory(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": inv})
}

// invoke lets an operator trigger a capability on a mind's behalf (testing
// integrations, demonstrations). It goes through exactly the same path.
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Capability string         `json:"capability"`
		Target     string         `json:"target"`
		Params     map[string]any `json:"params"`
		Reason     string         `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.deps.Capabilities.Invoke(r.Context(), capabilities.Invocation{MindID: m.ID, Capability: in.Capability, Target: in.Target, Params: in.Params, Reason: firstNonEmpty(in.Reason, "operator "+actor(r)+" invoked it")})
	if err != nil {
		fail(w, r, capErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "capability.invoke", "mind", m.ID.String(), map[string]any{"capability": in.Capability, "target": in.Target, "status": out.Status})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) approvalsList(w http.ResponseWriter, r *http.Request) {
	var mind ids.ID
	if v := r.URL.Query().Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			mind = m.ID
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.deps.Capabilities.Approvals(r.Context(), mind, r.URL.Query().Get("status"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []capabilities.Approval{}
	}
	writeJSON(w, http.StatusOK, page[capabilities.Approval]{Items: list})
}

func (s *Server) approvalGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	a, err := s.deps.Capabilities.GetApproval(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// Decide approves or denies; shared with the UI.
func (s *Server) Decide(r *http.Request, id ids.ID, decision, note string, params map[string]any) (any, error) {
	switch decision {
	case "approve", "session":
		out, err := s.deps.Capabilities.Approve(r.Context(), id, actor(r), note, decision == "session", params)
		if err != nil {
			return nil, capErr(err)
		}
		s.deps.Audit.Log(r.Context(), "approval.approve", "approval", id.String(), map[string]any{"for_session": decision == "session", "modified": params != nil, "status": out.Status})
		return out, nil
	case "deny":
		a, err := s.deps.Capabilities.Deny(r.Context(), id, actor(r), note)
		if err != nil {
			return nil, capErr(err)
		}
		s.deps.Audit.Log(r.Context(), "approval.deny", "approval", id.String(), map[string]any{"note": note})
		return a, nil
	}
	return nil, ValidationError("decision must be approve, session or deny")
}

func (s *Server) approvalApprove(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		Note       string          `json:"note"`
		ForSession bool            `json:"for_session"`
		Params     json.RawMessage `json:"params"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, r, err)
			return
		}
	}
	var params map[string]any
	if len(in.Params) > 0 {
		if err := json.Unmarshal(in.Params, &params); err != nil {
			fail(w, r, ValidationError("params must be a JSON object"))
			return
		}
	}
	decision := "approve"
	if in.ForSession {
		decision = "session"
	}
	out, err := s.Decide(r, id, decision, in.Note, params)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) approvalDeny(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		decode(r, &in) //nolint:errcheck
	}
	out, err := s.Decide(r, id, "deny", in.Note, nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) grantsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Capabilities.Grants(r.Context(), ids.Nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []capabilities.Grant{}
	}
	writeJSON(w, http.StatusOK, page[capabilities.Grant]{Items: list})
}

func (s *Server) grantRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.Capabilities.RevokeGrant(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "grant.revoke", "grant", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) executionsList(w http.ResponseWriter, r *http.Request) {
	var mind ids.ID
	if v := r.URL.Query().Get("mind"); v != "" {
		if m, err := s.deps.Minds.Resolve(r.Context(), v); err == nil {
			mind = m.ID
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.deps.Capabilities.Executions(r.Context(), mind, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []capabilities.Execution{}
	}
	writeJSON(w, http.StatusOK, page[capabilities.Execution]{Items: list})
}
