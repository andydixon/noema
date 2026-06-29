package ui

import (
	"bytes"
	"net/http"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) graphRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/graph", u.require(auth.RoleObserver, u.graphPage))
	m.Handle("GET /minds/{mind}/entities/{id}", u.require(auth.RoleObserver, u.entityPage))
	m.Handle("POST /minds/{mind}/entities/{id}/merge", u.require(auth.RoleOperator, u.entityMerge))
	m.Handle("POST /minds/{mind}/entities/{id}/distinct", u.require(auth.RoleOperator, u.entityDistinct))
	m.Handle("POST /minds/{mind}/entities/{id}/update", u.require(auth.RoleOperator, u.entityUpdate))
}

func (u *UI) graphPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	st, _ := u.deps.Graph.Stats(r.Context(), m.ID)
	top, _ := u.deps.Graph.Search(r.Context(), m.ID, "", graph.Kind(r.URL.Query().Get("kind")), 25)
	u.render(w, r, "graph", m.DisplayName+" · Relationships", map[string]any{"Mind": m, "Stats": st, "Entities": top, "Kind": r.URL.Query().Get("kind"), "Focus": r.URL.Query().Get("focus")})
}

func (u *UI) entityPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such entity.")
		return
	}
	d, err := u.entF(r, m.ID, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such entity.")
		return
	}
	data := map[string]any{"Mind": m, "Entity": d.Entity, "Edges": d.Edges, "Links": d.Links}
	if r.URL.Query().Get("partial") == "1" {
		// Rendered into the graph page's side panel by graph.js.
		t := u.pages["entity"]
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "entity_panel", View{Data: data}); err != nil {
			http.Error(w, "render failed", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		buf.WriteTo(w) //nolint:errcheck
		return
	}
	u.render(w, r, "entity", d.Entity.Name, data)
}

func (u *UI) entity(w http.ResponseWriter, r *http.Request) (api.EntityDetail, bool) {
	m, ok := u.mind(w, r)
	if !ok {
		return api.EntityDetail{}, false
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such entity.")
		return api.EntityDetail{}, false
	}
	d, err := u.entF(r, m.ID, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such entity.")
		return api.EntityDetail{}, false
	}
	return d, true
}

func (u *UI) entityMerge(w http.ResponseWriter, r *http.Request) {
	m, _ := u.mind(w, r)
	d, ok := u.entity(w, r)
	if !ok {
		return
	}
	if err := u.mergeF(r, m.ID, d.Entity, r.PostFormValue("other")); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/entities/"+d.Entity.ID.String())
		return
	}
	u.flash(w, r, "ok", "Entities merged; the old name is now an alias.", "/minds/"+m.Slug+"/entities/"+d.Entity.ID.String())
}

func (u *UI) entityDistinct(w http.ResponseWriter, r *http.Request) {
	m, _ := u.mind(w, r)
	d, ok := u.entity(w, r)
	if !ok {
		return
	}
	if err := u.distinctF(r, m.ID, d.Entity, r.PostFormValue("other")); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/entities/"+d.Entity.ID.String())
		return
	}
	u.flash(w, r, "ok", "Recorded as different entities.", "/minds/"+m.Slug+"/entities/"+d.Entity.ID.String())
}

func (u *UI) entityUpdate(w http.ResponseWriter, r *http.Request) {
	m, _ := u.mind(w, r)
	d, ok := u.entity(w, r)
	if !ok {
		return
	}
	if err := u.deps.Graph.Update(r.Context(), m.ID, d.Entity.ID, graph.Kind(r.PostFormValue("kind")), r.PostFormValue("description")); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/entities/"+d.Entity.ID.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "entity.update", "entity", d.Entity.ID.String(), map[string]any{"kind": r.PostFormValue("kind")})
	u.flash(w, r, "ok", "Entity updated.", "/minds/"+m.Slug+"/entities/"+d.Entity.ID.String())
}
