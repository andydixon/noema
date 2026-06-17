package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) mindRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds", require(auth.RoleObserver, s.mindsList))
	m.Handle("POST /api/v1/minds", require(auth.RoleAdministrator, s.mindsCreate))
	m.Handle("GET /api/v1/minds/{mind}", require(auth.RoleObserver, s.mindGet))
	m.Handle("PATCH /api/v1/minds/{mind}", require(auth.RoleAdministrator, s.mindPatch))
	m.Handle("POST /api/v1/minds/{mind}/clone", require(auth.RoleAdministrator, s.mindClone))
	m.Handle("GET /api/v1/minds/{mind}/selfmodel", require(auth.RoleObserver, s.mindSelfModel))
	m.Handle("GET /api/v1/minds/{mind}/personality", require(auth.RoleObserver, s.personalityGet))
	m.Handle("PUT /api/v1/minds/{mind}/personality", require(auth.RoleAdministrator, s.personalityPut))
	m.Handle("GET /api/v1/minds/{mind}/personality/brief", require(auth.RoleObserver, s.personalityBrief))
	m.Handle("GET /api/v1/minds/{mind}/personality/versions", require(auth.RoleObserver, s.personalityVersions))
	m.Handle("GET /api/v1/minds/{mind}/personality/versions/{n}", require(auth.RoleObserver, s.personalityVersion))
	m.Handle("POST /api/v1/minds/{mind}/personality/versions/{n}/restore", require(auth.RoleAdministrator, s.personalityRestore))
	m.Handle("GET /api/v1/minds/{mind}/personality/diff", require(auth.RoleObserver, s.personalityDiff))
	m.Handle("POST /api/v1/minds/{mind}/personality/template", require(auth.RoleAdministrator, s.personalityTemplate))
	m.Handle("GET /api/v1/personality-templates", require(auth.RoleObserver, s.templatesList))
	m.Handle("POST /api/v1/personality-templates", require(auth.RoleAdministrator, s.templatesCreate))
	m.Handle("GET /api/v1/personality-templates/{id}", require(auth.RoleObserver, s.templateGet))
	m.Handle("PUT /api/v1/personality-templates/{id}", require(auth.RoleAdministrator, s.templatePut))
	m.Handle("DELETE /api/v1/personality-templates/{id}", require(auth.RoleAdministrator, s.templateDelete))
}

// mind resolves the {mind} path value (slug or id) or writes an error.
func (s *Server) mind(w http.ResponseWriter, r *http.Request) (minds.Mind, bool) {
	m, err := s.deps.Minds.Resolve(r.Context(), r.PathValue("mind"))
	if err != nil {
		fail(w, r, err)
		return minds.Mind{}, false
	}
	return m, true
}

func actor(r *http.Request) string {
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		return p.User.Username
	}
	return "system"
}

func mindErr(err error) error {
	if errors.Is(err, minds.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) mindsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Minds.List(r.Context(), r.URL.Query().Get("include_archived") == "true")
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []minds.Mind{}
	}
	writeJSON(w, http.StatusOK, page[minds.Mind]{Items: list})
}

func (s *Server) mindsCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug        string                 `json:"slug"`
		DisplayName string                 `json:"display_name"`
		Personality *minds.Personality     `json:"personality"`
		TemplateID  *ids.ID                `json:"template_id"`
		Config      *minds.CognitiveConfig `json:"config"`
		Notes       string                 `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	m, err := s.deps.Minds.Create(r.Context(), minds.CreateParams{Slug: in.Slug, DisplayName: in.DisplayName, Personality: in.Personality,
		TemplateID: in.TemplateID, Config: in.Config, Notes: in.Notes, CreatedBy: actor(r)})
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.create", "mind", m.ID.String(), map[string]any{"slug": m.Slug})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) mindGet(w http.ResponseWriter, r *http.Request) {
	if m, ok := s.mind(w, r); ok {
		writeJSON(w, http.StatusOK, m)
	}
}

func (s *Server) mindPatch(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		DisplayName *string                `json:"display_name"`
		Notes       *string                `json:"notes"`
		Status      *minds.Status          `json:"status"`
		Config      *minds.CognitiveConfig `json:"config"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	out, err := s.deps.Minds.Update(r.Context(), m.ID, minds.UpdateParams{DisplayName: in.DisplayName, Notes: in.Notes, Status: in.Status, Config: in.Config})
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.update", "mind", m.ID.String(), in)
	if in.Status != nil {
		s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "mind.status", "Operator "+actor(r)+" set status to "+string(*in.Status), map[string]any{"status": *in.Status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mindClone(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	c, err := s.deps.Minds.Clone(r.Context(), m.ID, in.Slug, in.DisplayName, actor(r))
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.clone", "mind", c.ID.String(), map[string]any{"source": m.Slug, "slug": c.Slug})
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) mindSelfModel(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	sm, err := s.deps.Minds.GetSelfModel(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sm)
}

func (s *Server) personalityGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	v, err := s.deps.Minds.GetVersion(r.Context(), m.ID, m.PersonalityVersion)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) personalityBrief(w http.ResponseWriter, r *http.Request) {
	if m, ok := s.mind(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]string{"brief": m.Personality.Brief()})
	}
}

func (s *Server) personalityPut(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Personality minds.Personality `json:"personality"`
		ChangeNote  string            `json:"change_note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	v, err := s.deps.Minds.SavePersonality(r.Context(), m.ID, in.Personality, in.ChangeNote, actor(r))
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "personality.update", "mind", m.ID.String(), map[string]any{"version": v.Version, "note": in.ChangeNote, "changes": minds.Diff(m.Personality, v.Personality)})
	s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "personality.update", fmt.Sprintf("Operator %s updated personality to version %d: %s", actor(r), v.Version, in.ChangeNote), map[string]any{"version": v.Version, "changes": minds.Diff(m.Personality, v.Personality)})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) personalityVersions(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	vs, err := s.deps.Minds.ListVersions(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page[minds.Version]{Items: vs})
}

func (s *Server) personalityVersion(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		fail(w, r, ValidationError("bad version"))
		return
	}
	v, err := s.deps.Minds.GetVersion(r.Context(), m.ID, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) personalityRestore(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		fail(w, r, ValidationError("bad version"))
		return
	}
	v, err := s.deps.Minds.Restore(r.Context(), m.ID, n, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "personality.restore", "mind", m.ID.String(), map[string]any{"restored": n, "version": v.Version})
	s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "personality.restore", fmt.Sprintf("Operator %s restored personality version %d as version %d", actor(r), n, v.Version), map[string]any{"restored": n, "version": v.Version})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) personalityDiff(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	a, errA := strconv.Atoi(r.URL.Query().Get("a"))
	b, errB := strconv.Atoi(r.URL.Query().Get("b"))
	if errA != nil || errB != nil {
		fail(w, r, ValidationError("a and b version numbers required"))
		return
	}
	va, err := s.deps.Minds.GetVersion(r.Context(), m.ID, a)
	if err != nil {
		fail(w, r, err)
		return
	}
	vb, err := s.deps.Minds.GetVersion(r.Context(), m.ID, b)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"a": a, "b": b, "changes": minds.Diff(va.Personality, vb.Personality)})
}

func (s *Server) personalityTemplate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		TemplateID *ids.ID `json:"template_id"` // null detaches
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	v, err := s.deps.Minds.ApplyTemplate(r.Context(), m.ID, in.TemplateID, actor(r))
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "personality.template", "mind", m.ID.String(), map[string]any{"template_id": in.TemplateID, "version": v.Version})
	s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "personality.template", "Operator "+actor(r)+": "+v.ChangeNote, map[string]any{"version": v.Version})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) templatesList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Minds.ListTemplates(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []minds.Template{}
	}
	writeJSON(w, http.StatusOK, page[minds.Template]{Items: list})
}

type templateIn struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Personality minds.Personality `json:"personality"`
}

func (s *Server) templatesCreate(w http.ResponseWriter, r *http.Request) {
	var in templateIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	t, err := s.deps.Minds.CreateTemplate(r.Context(), in.Name, in.Description, in.Personality)
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "template.create", "personality_template", t.ID.String(), map[string]any{"name": t.Name})
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) templateGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	t, err := s.deps.Minds.GetTemplate(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) templatePut(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in templateIn
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	t, err := s.deps.Minds.UpdateTemplate(r.Context(), id, in.Name, in.Description, in.Personality)
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "template.update", "personality_template", t.ID.String(), map[string]any{"name": t.Name})
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) templateDelete(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.Minds.DeleteTemplate(r.Context(), id); err != nil {
		fail(w, r, mindErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "template.delete", "personality_template", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}
