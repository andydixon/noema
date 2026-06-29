package ui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/integrations"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) integrationRoutes(m *http.ServeMux) {
	m.Handle("GET /integrations", u.require(auth.RoleObserver, u.integrationsPage))
	m.Handle("POST /integrations", u.require(auth.RoleAdministrator, u.integrationCreate))
	m.Handle("GET /integrations/{id}", u.require(auth.RoleObserver, u.integrationPage))
	m.Handle("POST /integrations/{id}/run", u.require(auth.RoleOperator, u.integrationRun))
	m.Handle("POST /integrations/{id}/toggle", u.require(auth.RoleAdministrator, u.integrationToggle))
	m.Handle("POST /integrations/{id}/delete", u.require(auth.RoleAdministrator, u.integrationDelete))
}

func (u *UI) integrationsPage(w http.ResponseWriter, r *http.Request) {
	names, _ := u.mindNames(r)
	list, _ := u.deps.Integrations.Store.List(r.Context(), ids.Nil)
	mindsList, _ := u.deps.Minds.List(r.Context(), false)
	u.render(w, r, "integrations", "Integrations", map[string]any{"Integrations": list, "MindNames": names, "Minds": mindsList})
}

// parseQueries reads "name = expr [gt|lt threshold]" lines.
func parseQueries(text string) []any {
	var out []any
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		q := map[string]any{"name": strings.TrimSpace(name)}
		rest = strings.TrimSpace(rest)
		for _, cmp := range []string{" gt ", " lt "} {
			if i := strings.LastIndex(rest, cmp); i >= 0 {
				if th, err := strconv.ParseFloat(strings.TrimSpace(rest[i+len(cmp):]), 64); err == nil {
					q["compare"], q["threshold"] = strings.TrimSpace(cmp), th
					rest = strings.TrimSpace(rest[:i])
				}
			}
		}
		q["expr"] = rest
		out = append(out, q)
	}
	return out
}

func (u *UI) integrationCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	m, err := u.deps.Minds.Resolve(r.Context(), r.PostFormValue("mind"))
	if err != nil {
		u.flash(w, r, "error", "Choose a mind.", "/integrations")
		return
	}
	kind := integrations.Kind(r.PostFormValue("kind"))
	cfg := map[string]any{}
	if v, err := strconv.Atoi(r.PostFormValue("interval_seconds")); err == nil {
		cfg["interval_seconds"] = float64(v)
	}
	target := strings.TrimSpace(r.PostFormValue("target"))
	switch kind {
	case integrations.ResearchFolder:
		cfg["path"] = target
	case integrations.Git:
		cfg["url"] = target
	case integrations.Prometheus:
		cfg["endpoint"] = target
		cfg["queries"] = parseQueries(r.PostFormValue("queries"))
		cfg["allow_private"] = r.PostFormValue("allow_private") != ""
	}
	ref, err := u.keyRef(r.PostFormValue("secret"))
	if err != nil {
		u.flash(w, r, "error", "Cannot store secret: "+err.Error(), "/integrations")
		return
	}
	i, err := u.deps.Integrations.Store.Create(r.Context(), integrations.Input{MindID: m.ID, Kind: kind, Name: r.PostFormValue("name"), Config: cfg, SecretRef: ref, Enabled: true})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/integrations")
		return
	}
	u.deps.Audit.Log(r.Context(), "integration.create", "integration", i.ID.String(), map[string]any{"kind": i.Kind, "name": i.Name, "mind": m.Slug})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "integration.create", "Operator "+username(r)+" connected "+string(i.Kind)+" integration "+i.Name, map[string]any{"integration_id": i.ID})
	http.Redirect(w, r, "/integrations/"+i.ID.String(), http.StatusSeeOther)
}

func (u *UI) integ(w http.ResponseWriter, r *http.Request) (integrations.Integration, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such integration.")
		return integrations.Integration{}, false
	}
	i, err := u.deps.Integrations.Store.Get(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, 404, "No such integration.")
		return integrations.Integration{}, false
	}
	return i, true
}

func (u *UI) integrationPage(w http.ResponseWriter, r *http.Request) {
	i, ok := u.integ(w, r)
	if !ok {
		return
	}
	runs, _ := u.deps.Integrations.Store.Runs(r.Context(), i.ID, 15)
	docs, _ := u.deps.Integrations.Store.Documents(r.Context(), i.ID, 50)
	name := ""
	if m, err := u.deps.Minds.Get(r.Context(), i.MindID); err == nil {
		name = m.DisplayName
	}
	u.render(w, r, "integration", "Integration "+i.Name, map[string]any{"Integration": i, "Runs": runs, "Documents": docs, "MindName": name})
}

func (u *UI) integrationRun(w http.ResponseWriter, r *http.Request) {
	i, ok := u.integ(w, r)
	if !ok {
		return
	}
	run, err := u.deps.Integrations.RunOne(r.Context(), i.ID)
	u.deps.Audit.Log(r.Context(), "integration.run", "integration", i.ID.String(), map[string]any{"observations": run.Observations, "error": run.Error})
	if err != nil {
		u.flash(w, r, "error", "Run failed: "+err.Error(), "/integrations/"+i.ID.String())
		return
	}
	u.flash(w, r, "ok", "Run finished: "+itoa(run.Items)+" item(s), "+itoa(run.Observations)+" observation(s).", "/integrations/"+i.ID.String())
}

func (u *UI) integrationToggle(w http.ResponseWriter, r *http.Request) {
	i, ok := u.integ(w, r)
	if !ok {
		return
	}
	if _, err := u.deps.Integrations.Store.Update(r.Context(), i.ID, integrations.Input{Name: i.Name, Config: i.Config, Trust: i.Trust, Enabled: !i.Enabled}); err != nil {
		u.flash(w, r, "error", err.Error(), "/integrations/"+i.ID.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "integration.update", "integration", i.ID.String(), map[string]any{"enabled": !i.Enabled})
	u.flash(w, r, "ok", "Integration updated.", "/integrations/"+i.ID.String())
}

func (u *UI) integrationDelete(w http.ResponseWriter, r *http.Request) {
	i, ok := u.integ(w, r)
	if !ok {
		return
	}
	if err := u.deps.Integrations.Store.Delete(r.Context(), i.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/integrations")
		return
	}
	u.deps.Audit.Log(r.Context(), "integration.delete", "integration", i.ID.String(), map[string]any{"name": i.Name})
	u.flash(w, r, "ok", "Integration deleted.", "/integrations")
}
