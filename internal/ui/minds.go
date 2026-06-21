package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) mindRoutes(m *http.ServeMux) {
	m.Handle("GET /minds", u.require(auth.RoleObserver, u.mindsList))
	m.Handle("POST /minds", u.require(auth.RoleAdministrator, u.mindsCreate))
	m.Handle("GET /minds/{mind}", u.require(auth.RoleObserver, u.mindOverview))
	m.Handle("POST /minds/{mind}/status", u.require(auth.RoleAdministrator, u.mindStatus))
	m.Handle("POST /minds/{mind}/notes", u.require(auth.RoleAdministrator, u.mindNotes))
	m.Handle("POST /minds/{mind}/clone", u.require(auth.RoleAdministrator, u.mindClone))
	m.Handle("GET /minds/{mind}/personality", u.require(auth.RoleObserver, u.personalityEditor))
	m.Handle("GET /minds/{mind}/personality.json", u.require(auth.RoleObserver, u.personalityExport))
	m.Handle("POST /minds/{mind}/personality", u.require(auth.RoleAdministrator, u.personalitySave))
	m.Handle("POST /minds/{mind}/personality/preview", u.require(auth.RoleObserver, u.personalityPreview))
	m.Handle("POST /minds/{mind}/personality/import", u.require(auth.RoleAdministrator, u.personalityImport))
	m.Handle("POST /minds/{mind}/personality/template", u.require(auth.RoleAdministrator, u.personalityTemplate))
	m.Handle("POST /minds/{mind}/personality/versions/{n}/restore", u.require(auth.RoleAdministrator, u.personalityRestore))
	m.Handle("GET /minds/{mind}/personality/diff", u.require(auth.RoleObserver, u.personalityDiff))
	m.Handle("GET /minds/{mind}/workspace", u.require(auth.RoleObserver, u.workspacePage))
	m.Handle("POST /minds/{mind}/workspace/control", u.require(auth.RoleOperator, u.workspaceControl))
	m.Handle("POST /minds/{mind}/workspace/weights", u.require(auth.RoleAdministrator, u.workspaceWeights))
	m.Handle("GET /personality-templates", u.require(auth.RoleObserver, u.templates))
	m.Handle("POST /personality-templates", u.require(auth.RoleAdministrator, u.templateCreate))
	m.Handle("GET /personality-templates/{id}", u.require(auth.RoleObserver, u.templateEdit))
	m.Handle("POST /personality-templates/{id}", u.require(auth.RoleAdministrator, u.templateSave))
	m.Handle("POST /personality-templates/{id}/delete", u.require(auth.RoleAdministrator, u.templateDelete))
}

func (u *UI) mind(w http.ResponseWriter, r *http.Request) (minds.Mind, bool) {
	m, err := u.deps.Minds.Resolve(r.Context(), r.PathValue("mind"))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			u.errorPage(w, r, http.StatusNotFound, "No such mind.")
		} else {
			u.errorPage(w, r, http.StatusInternalServerError, "Could not load mind.")
		}
		return minds.Mind{}, false
	}
	return m, true
}

func username(r *http.Request) string {
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		return p.User.Username
	}
	return "system"
}

func (u *UI) mindsList(w http.ResponseWriter, r *http.Request) {
	list, err := u.deps.Minds.List(r.Context(), r.URL.Query().Get("include_archived") == "1")
	if err != nil {
		u.errorPage(w, r, 500, "Could not list minds.")
		return
	}
	tmpls, _ := u.deps.Minds.ListTemplates(r.Context())
	u.render(w, r, "minds", "Minds", map[string]any{"Minds": list, "Templates": tmpls})
}

func (u *UI) mindsCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	p := minds.CreateParams{Slug: r.PostFormValue("slug"), DisplayName: strings.TrimSpace(r.PostFormValue("display_name")), CreatedBy: username(r)}
	if t := r.PostFormValue("template_id"); t != "" {
		if id, err := ids.Parse(t); err == nil {
			p.TemplateID = &id
		}
	}
	m, err := u.deps.Minds.Create(r.Context(), p)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, storage.ErrConflict) {
			msg = "A mind with that slug already exists."
		}
		u.flash(w, r, "error", msg, "/minds")
		return
	}
	u.deps.Audit.Log(r.Context(), "mind.create", "mind", m.ID.String(), map[string]any{"slug": m.Slug})
	u.flash(w, r, "ok", "Mind created. Shape its personality next.", "/minds/"+m.Slug+"/personality")
}

func (u *UI) mindOverview(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	sm, _ := u.deps.Minds.GetSelfModel(r.Context(), m.ID)
	st, _ := u.deps.Events.Stats(r.Context(), m.ID, time.Now().Add(-24*time.Hour))
	ms, _ := u.deps.Memory.Stats(r.Context(), m.ID)
	bs, _ := u.deps.Beliefs.Stats(r.Context(), m.ID)
	top, _ := u.deps.Beliefs.List(r.Context(), m.ID, beliefs.Filter{Limit: 8})
	u.render(w, r, "mind", m.DisplayName, map[string]any{"Mind": m, "Brief": m.Personality.Brief(), "SelfModel": sm, "Stats": st, "MemStats": ms, "BeliefStats": bs, "Beliefs": top})
}

func (u *UI) mindStatus(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	st := minds.Status(r.PostFormValue("status"))
	if _, err := u.deps.Minds.Update(r.Context(), m.ID, minds.UpdateParams{Status: &st}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug)
		return
	}
	u.deps.Audit.Log(r.Context(), "mind.status", "mind", m.ID.String(), map[string]any{"status": st})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "mind.status", "Operator "+username(r)+" set status to "+string(st), map[string]any{"status": st})
	u.deps.Brain.Reconcile(r.Context()) //nolint:errcheck
	u.flash(w, r, "ok", "Mind is now "+string(st)+".", "/minds/"+m.Slug)
}

func (u *UI) mindNotes(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	notes := r.PostFormValue("notes")
	if _, err := u.deps.Minds.Update(r.Context(), m.ID, minds.UpdateParams{Notes: &notes}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug)
		return
	}
	u.deps.Audit.Log(r.Context(), "mind.notes", "mind", m.ID.String(), nil)
	u.flash(w, r, "ok", "Notes saved.", "/minds/"+m.Slug)
}

func (u *UI) mindClone(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	c, err := u.deps.Minds.Clone(r.Context(), m.ID, r.PostFormValue("slug"), strings.TrimSpace(r.PostFormValue("display_name")), username(r))
	if err != nil {
		msg := err.Error()
		if errors.Is(err, storage.ErrConflict) {
			msg = "A mind with that slug already exists."
		}
		u.flash(w, r, "error", msg, "/minds/"+m.Slug)
		return
	}
	u.deps.Audit.Log(r.Context(), "mind.clone", "mind", c.ID.String(), map[string]any{"source": m.Slug, "slug": c.Slug})
	u.flash(w, r, "ok", "Cloned into "+c.DisplayName+".", "/minds/"+c.Slug)
}

func (u *UI) personalityData(r *http.Request, m minds.Mind) map[string]any {
	versions, _ := u.deps.Minds.ListVersions(r.Context(), m.ID)
	tmpls, _ := u.deps.Minds.ListTemplates(r.Context())
	data := map[string]any{"Mind": m, "Versions": versions, "Templates": tmpls}
	if m.TemplateID != nil {
		if t, err := u.deps.Minds.GetTemplate(r.Context(), *m.TemplateID); err == nil {
			data["Template"] = t
		}
		if v, err := u.deps.Minds.GetVersion(r.Context(), m.ID, m.PersonalityVersion); err == nil {
			data["Overrides"] = flattenKeys("", v.Overrides)
		}
	}
	return data
}

func flattenKeys(prefix string, m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			for kk, vv := range flattenKeys(path, sub) {
				out[kk] = vv
			}
			continue
		}
		out[path] = v
	}
	return out
}

func (u *UI) personalityEditor(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	u.render(w, r, "personality", m.DisplayName+" · Personality", u.personalityData(r, m))
}

func (u *UI) personalityExport(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+m.Slug+`-personality-v`+strconv.Itoa(m.PersonalityVersion)+`.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(map[string]any{"schema": "noema.personality/1", "mind": m.Slug, "version": m.PersonalityVersion, "personality": m.Personality}) //nolint:errcheck
}

// personalityFromForm reads the structured editor fields.
func personalityFromForm(r *http.Request) minds.Personality {
	f := r.PostForm
	num := func(k string) float64 { v, _ := strconv.ParseFloat(f.Get(k), 64); return v }
	p := minds.Personality{
		Identity: minds.Identity{Name: f.Get("identity.name"), DisplayName: f.Get("identity.display_name"), Description: f.Get("identity.description"),
			Role: f.Get("identity.role"), Pronouns: f.Get("identity.pronouns"), Backstory: f.Get("identity.backstory"), Origin: f.Get("identity.origin")},
		Values: map[string]float64{}, Traits: map[string]float64{},
		Communication: minds.Communication{Language: f.Get("communication.language"), Dialect: f.Get("communication.dialect"), Formality: num("communication.formality"),
			Verbosity: num("communication.verbosity"), HumourStyle: f.Get("communication.humour_style"), Terminology: f.Get("communication.terminology")},
		Behaviour: minds.Behaviour{ChallengeAssumptions: f.Get("behaviour.challenge_assumptions") != "", AskFollowUps: f.Get("behaviour.ask_follow_ups") != "",
			PreferEvidence: f.Get("behaviour.prefer_evidence") != "", CiteUncertainty: f.Get("behaviour.cite_uncertainty") != "",
			OfferAlternatives: f.Get("behaviour.offer_alternatives") != "", NotifyProactively: f.Get("behaviour.notify_proactively") != ""},
		FreeText: f.Get("free_text"),
	}
	for _, k := range minds.ValueKeys {
		p.Values[k] = num("values." + k)
	}
	for _, k := range minds.TraitKeys {
		p.Traits[k] = num("traits." + k)
	}
	p.Normalise()
	return p
}

func (u *UI) personalitySave(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	p := personalityFromForm(r)
	v, err := u.deps.Minds.SavePersonality(r.Context(), m.ID, p, r.PostFormValue("change_note"), username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/personality")
		return
	}
	u.deps.Audit.Log(r.Context(), "personality.update", "mind", m.ID.String(), map[string]any{"version": v.Version, "note": v.ChangeNote, "changes": minds.Diff(m.Personality, v.Personality)})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "personality.update", "Operator "+username(r)+" updated personality to version "+strconv.Itoa(v.Version)+": "+v.ChangeNote, map[string]any{"version": v.Version, "changes": minds.Diff(m.Personality, v.Personality)})
	u.flash(w, r, "ok", "Saved as version "+strconv.Itoa(v.Version)+".", "/minds/"+m.Slug+"/personality")
}

func (u *UI) personalityPreview(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	p := personalityFromForm(r)
	m.Personality = p // show the edited values back in the form
	data := u.personalityData(r, m)
	data["Preview"] = p.Brief()
	u.render(w, r, "personality", m.DisplayName+" · Personality", data)
}

func (u *UI) personalityImport(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("json"))
	if len(raw) > 64<<10 {
		u.flash(w, r, "error", "Import too large.", "/minds/"+m.Slug+"/personality")
		return
	}
	// Accept either a bare personality or the export envelope.
	var env struct {
		Personality *minds.Personality `json:"personality"`
	}
	var p minds.Personality
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || p.Identity.Name == "" {
		if err := json.Unmarshal([]byte(raw), &env); err != nil || env.Personality == nil {
			u.flash(w, r, "error", "Not a valid personality document.", "/minds/"+m.Slug+"/personality")
			return
		}
		p = *env.Personality
	}
	v, err := u.deps.Minds.SavePersonality(r.Context(), m.ID, p, "imported", username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/personality")
		return
	}
	u.deps.Audit.Log(r.Context(), "personality.import", "mind", m.ID.String(), map[string]any{"version": v.Version})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "personality.import", "Operator "+username(r)+" imported personality as version "+strconv.Itoa(v.Version), map[string]any{"version": v.Version})
	u.flash(w, r, "ok", "Imported as version "+strconv.Itoa(v.Version)+".", "/minds/"+m.Slug+"/personality")
}

func (u *UI) personalityTemplate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	var tid *ids.ID
	if t := r.PostFormValue("template_id"); t != "" {
		id, err := ids.Parse(t)
		if err != nil {
			u.flash(w, r, "error", "Bad template.", "/minds/"+m.Slug+"/personality")
			return
		}
		tid = &id
	}
	v, err := u.deps.Minds.ApplyTemplate(r.Context(), m.ID, tid, username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/personality")
		return
	}
	u.deps.Audit.Log(r.Context(), "personality.template", "mind", m.ID.String(), map[string]any{"template_id": tid, "version": v.Version})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "personality.template", "Operator "+username(r)+": "+v.ChangeNote, map[string]any{"version": v.Version})
	u.flash(w, r, "ok", v.ChangeNote+" (version "+strconv.Itoa(v.Version)+").", "/minds/"+m.Slug+"/personality")
}

func (u *UI) personalityRestore(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		u.flash(w, r, "error", "Bad version.", "/minds/"+m.Slug+"/personality")
		return
	}
	v, err := u.deps.Minds.Restore(r.Context(), m.ID, n, username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/personality")
		return
	}
	u.deps.Audit.Log(r.Context(), "personality.restore", "mind", m.ID.String(), map[string]any{"restored": n, "version": v.Version})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "personality.restore", "Operator "+username(r)+" restored personality version "+strconv.Itoa(n)+" as version "+strconv.Itoa(v.Version), map[string]any{"restored": n, "version": v.Version})
	u.flash(w, r, "ok", "Restored version "+strconv.Itoa(n)+" as version "+strconv.Itoa(v.Version)+".", "/minds/"+m.Slug+"/personality")
}

func (u *UI) personalityDiff(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	a, _ := strconv.Atoi(r.URL.Query().Get("a"))
	b, _ := strconv.Atoi(r.URL.Query().Get("b"))
	va, errA := u.deps.Minds.GetVersion(r.Context(), m.ID, a)
	vb, errB := u.deps.Minds.GetVersion(r.Context(), m.ID, b)
	if errA != nil || errB != nil {
		u.errorPage(w, r, 404, "Version not found.")
		return
	}
	u.render(w, r, "personality_diff", "Personality diff", map[string]any{"Mind": m, "A": a, "B": b, "Changes": minds.Diff(va.Personality, vb.Personality)})
}

func (u *UI) templates(w http.ResponseWriter, r *http.Request) {
	list, err := u.deps.Minds.ListTemplates(r.Context())
	if err != nil {
		u.errorPage(w, r, 500, "Could not list templates.")
		return
	}
	u.render(w, r, "templates", "Personality templates", map[string]any{"Templates": list})
}

func (u *UI) templateCreate(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("name")
	t, err := u.deps.Minds.CreateTemplate(r.Context(), name, r.PostFormValue("description"), minds.DefaultPersonality(name))
	if err != nil {
		msg := err.Error()
		if errors.Is(err, storage.ErrConflict) {
			msg = "A template with that name exists."
		}
		u.flash(w, r, "error", msg, "/personality-templates")
		return
	}
	u.deps.Audit.Log(r.Context(), "template.create", "personality_template", t.ID.String(), map[string]any{"name": t.Name})
	http.Redirect(w, r, "/personality-templates/"+t.ID.String(), http.StatusSeeOther)
}

func (u *UI) template(w http.ResponseWriter, r *http.Request) (minds.Template, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such template.")
		return minds.Template{}, false
	}
	t, err := u.deps.Minds.GetTemplate(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, 404, "No such template.")
		return minds.Template{}, false
	}
	return t, true
}

func (u *UI) templateEdit(w http.ResponseWriter, r *http.Request) {
	t, ok := u.template(w, r)
	if !ok {
		return
	}
	u.render(w, r, "template_edit", "Template "+t.Name, map[string]any{"Template": t, "Brief": t.Personality.Brief()})
}

func (u *UI) templateSave(w http.ResponseWriter, r *http.Request) {
	t, ok := u.template(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	p := personalityFromForm(r)
	if _, err := u.deps.Minds.UpdateTemplate(r.Context(), t.ID, r.PostFormValue("name"), r.PostFormValue("description"), p); err != nil {
		u.flash(w, r, "error", err.Error(), "/personality-templates/"+t.ID.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "template.update", "personality_template", t.ID.String(), map[string]any{"changes": minds.Diff(t.Personality, p)})
	u.flash(w, r, "ok", "Template saved. Re-apply it to minds that should pick up the change.", "/personality-templates/"+t.ID.String())
}

func (u *UI) templateDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := u.template(w, r)
	if !ok {
		return
	}
	if err := u.deps.Minds.DeleteTemplate(r.Context(), t.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/personality-templates")
		return
	}
	u.deps.Audit.Log(r.Context(), "template.delete", "personality_template", t.ID.String(), map[string]any{"name": t.Name})
	u.flash(w, r, "ok", "Template deleted.", "/personality-templates")
}

func (u *UI) workspacePage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	v, err := u.wsF(r, m)
	if err != nil {
		u.errorPage(w, r, 500, "Could not load workspace: "+err.Error())
		return
	}
	u.render(w, r, "workspace", m.DisplayName+" · Workspace", map[string]any{"Mind": m, "View": v})
}

func (u *UI) workspaceControl(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	action := r.PostFormValue("action")
	var err error
	switch action {
	case "cycle", "pause", "resume":
		err = u.deps.Brain.Command(r.Context(), m.ID, action, nil)
	case "reset":
		err = u.deps.Brain.Command(r.Context(), m.ID, "reset", map[string]bool{"workspace": true, "working_memory": true, "loops": true})
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/workspace")
		return
	}
	u.deps.Audit.Log(r.Context(), "mind."+action, "mind", m.ID.String(), nil)
	u.flash(w, r, "ok", "Done: "+action+".", "/minds/"+m.Slug+"/workspace")
}

func (u *UI) workspaceWeights(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	cfg := m.Config
	for _, f := range minds.AttentionFeatures {
		if v, err := strconv.ParseFloat(r.PostFormValue("w."+f), 64); err == nil {
			cfg.AttentionWeights[f] = v
		}
	}
	if v, err := strconv.Atoi(r.PostFormValue("workspace_capacity")); err == nil {
		cfg.WorkspaceCapacity = v
	}
	if v, err := strconv.Atoi(r.PostFormValue("working_memory_capacity")); err == nil {
		cfg.WorkingMemoryCapacity = v
	}
	if v, err := strconv.ParseFloat(r.PostFormValue("working_memory_decay"), 64); err == nil {
		cfg.WorkingMemoryDecay = v
	}
	if v, err := strconv.Atoi(r.PostFormValue("loop_detect_threshold")); err == nil {
		cfg.LoopDetectThreshold = v
	}
	if _, err := u.deps.Minds.Update(r.Context(), m.ID, minds.UpdateParams{Config: &cfg}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/workspace")
		return
	}
	u.deps.Audit.Log(r.Context(), "mind.config", "mind", m.ID.String(), map[string]any{"weights": cfg.AttentionWeights, "workspace_capacity": cfg.WorkspaceCapacity})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "mind.config", "Operator "+username(r)+" changed attention weights and capacities", map[string]any{"weights": cfg.AttentionWeights})
	u.deps.Brain.Command(r.Context(), m.ID, "reload", nil) //nolint:errcheck // not running is fine
	u.flash(w, r, "ok", "Configuration saved and applied.", "/minds/"+m.Slug+"/workspace")
}
