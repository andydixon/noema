package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
)

func (s *Server) promptRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/prompts", require(auth.RoleObserver, s.promptsList))
	m.Handle("POST /api/v1/prompts", require(auth.RoleAdministrator, s.promptsSave))
	m.Handle("GET /api/v1/prompts/{name}", require(auth.RoleObserver, s.promptGet))
	m.Handle("PUT /api/v1/prompts/{name}", require(auth.RoleAdministrator, s.promptsSave))
	m.Handle("GET /api/v1/prompts/{name}/versions", require(auth.RoleObserver, s.promptVersions))
	m.Handle("POST /api/v1/prompts/{name}/versions/{n}/restore", require(auth.RoleAdministrator, s.promptRestore))
	m.Handle("POST /api/v1/prompts/{name}/clone", require(auth.RoleAdministrator, s.promptClone))
	m.Handle("POST /api/v1/prompts/{name}/test", require(auth.RoleOperator, s.promptTest))
}

func promptErr(err error) error {
	if errors.Is(err, prompts.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) promptsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Prompts.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []prompts.Prompt{}
	}
	writeJSON(w, http.StatusOK, page[prompts.Prompt]{Items: list})
}

func (s *Server) promptGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.deps.Prompts.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) promptsSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Role        llm.Role        `json:"role"`
		System      string          `json:"system"`
		Template    string          `json:"template"`
		Schema      json.RawMessage `json:"schema"`
		Note        string          `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if n := r.PathValue("name"); n != "" {
		in.Name = n
	}
	p, err := s.deps.Prompts.Save(r.Context(), prompts.Input{Name: in.Name, Description: in.Description, Role: in.Role, System: in.System, Template: in.Template, Schema: in.Schema, Note: in.Note, By: actor(r)})
	if err != nil {
		fail(w, r, promptErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "prompt.save", "prompt", p.Name, map[string]any{"version": p.CurrentVersion, "note": in.Note})
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) promptVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.deps.Prompts.Versions(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if vs == nil {
		vs = []prompts.Version{}
	}
	writeJSON(w, http.StatusOK, page[prompts.Version]{Items: vs})
}

func (s *Server) promptRestore(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		fail(w, r, ValidationError("bad version"))
		return
	}
	p, err := s.deps.Prompts.Restore(r.Context(), r.PathValue("name"), n, actor(r))
	if err != nil {
		fail(w, r, promptErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "prompt.restore", "prompt", p.Name, map[string]any{"restored": n, "version": p.CurrentVersion})
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) promptClone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.deps.Prompts.Clone(r.Context(), r.PathValue("name"), in.Name, actor(r))
	if err != nil {
		fail(w, r, promptErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "prompt.clone", "prompt", p.Name, map[string]any{"from": r.PathValue("name")})
	writeJSON(w, http.StatusCreated, p)
}

// promptTest renders a prompt (optionally an unsaved draft) with sample data
// and, if requested, runs it through the role's model.
func (s *Server) promptTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Data     map[string]any  `json:"data"`
		System   *string         `json:"system"`
		Template *string         `json:"template"`
		Schema   json.RawMessage `json:"schema"`
		Run      bool            `json:"run"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.deps.Prompts.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.System != nil {
		p.System = *in.System
	}
	if in.Template != nil {
		p.Template = *in.Template
	}
	if len(in.Schema) > 0 {
		p.Schema = in.Schema
	}
	req, err := p.Render(in.Data)
	if err != nil {
		fail(w, r, promptErr(err))
		return
	}
	out := map[string]any{"rendered": req}
	if in.Run {
		req.Purpose = "prompt test by " + actor(r)
		resp, err := s.deps.LLM.Complete(r.Context(), req)
		if err != nil {
			out["error"] = err.Error()
		} else {
			out["response"] = resp
		}
		s.deps.Audit.Log(r.Context(), "prompt.test", "prompt", p.Name, map[string]any{"ran": true, "ok": err == nil})
	}
	writeJSON(w, http.StatusOK, out)
}
