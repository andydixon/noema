package ui

import (
	"net/http"
	"strconv"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/dreaming"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) dreamRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/dreams", u.require(auth.RoleObserver, u.dreamsPage))
	m.Handle("POST /minds/{mind}/dreams/run", u.require(auth.RoleOperator, u.dreamRun))
	m.Handle("POST /minds/{mind}/dreams/promote", u.require(auth.RoleOperator, u.dreamReview("promote")))
	m.Handle("POST /minds/{mind}/dreams/reject", u.require(auth.RoleOperator, u.dreamReview("reject")))
	m.Handle("POST /minds/{mind}/dreams/settings", u.require(auth.RoleAdministrator, u.dreamSettings))
	m.Handle("POST /dreams/pause", u.require(auth.RoleAdministrator, u.dreamPause))
}

func (u *UI) dreamsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	dreams, _ := u.deps.Dreaming.Dreams(r.Context(), m.ID, 20)
	pending, _ := u.deps.Dreaming.Hypotheses(r.Context(), m.ID, "proposed", 100)
	all, _ := u.deps.Dreaming.Hypotheses(r.Context(), m.ID, "", 60)
	var reviewed []dreaming.Hypothesis
	for _, h := range all {
		if h.Status != "proposed" {
			reviewed = append(reviewed, h)
		}
	}
	u.render(w, r, "dreams", m.DisplayName+" · Dreams", map[string]any{"Mind": m, "Dreams": dreams, "Pending": pending, "Reviewed": reviewed, "Paused": dreaming.IsPaused()})
}

func (u *UI) dreamRun(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	u.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"dreaming": true}) //nolint:errcheck
	d, err := u.deps.Dreaming.Run(r.Context(), m, "operator", username(r))
	u.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"awake": true}) //nolint:errcheck
	u.deps.Audit.Log(r.Context(), "dream.run", "mind", m.ID.String(), map[string]any{"dream": d.ID, "status": d.Status, "hypotheses": d.Hypotheses})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/dreams")
		return
	}
	u.flash(w, r, "ok", "Dream "+d.Status+": sampled "+itoa(d.Sampled)+", linked "+itoa(d.Associations)+", proposed "+itoa(d.Hypotheses)+" hypothesis(es)."+errSuffix(d.Error), "/minds/"+m.Slug+"/dreams")
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " (" + e + ")"
}

func (u *UI) dreamReview(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, ok := u.mind(w, r)
		if !ok {
			return
		}
		id, err := ids.Parse(r.PostFormValue("hypothesis_id"))
		if err != nil {
			u.flash(w, r, "error", "Bad hypothesis id.", "/minds/"+m.Slug+"/dreams")
			return
		}
		note := r.PostFormValue("note")
		var h dreaming.Hypothesis
		if action == "promote" {
			h, err = u.deps.Dreaming.Promote(r.Context(), m, id, note, username(r))
		} else {
			h, err = u.deps.Dreaming.Reject(r.Context(), m, id, note, username(r))
		}
		if err != nil {
			u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/dreams")
			return
		}
		u.deps.Audit.Log(r.Context(), "dream."+action, "belief", h.BeliefID.String(), map[string]any{"hypothesis": id, "note": note})
		u.flash(w, r, "ok", "Hypothesis "+h.Status+".", "/minds/"+m.Slug+"/dreams")
	}
}

func (u *UI) dreamSettings(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	cfg := m.Config
	cfg.DreamingEnabled = r.PostFormValue("enabled") != ""
	cfg.DreamHours = r.PostFormValue("hours")
	if d, err := time.ParseDuration(r.PostFormValue("interval")); err == nil {
		cfg.DreamInterval = minds.Duration(d)
	}
	cfg.DreamLLMCalls, _ = strconv.Atoi(r.PostFormValue("llm_calls"))
	cfg.DreamMaxMemories, _ = strconv.Atoi(r.PostFormValue("max_memories"))
	cfg.DreamMaxHypotheses, _ = strconv.Atoi(r.PostFormValue("max_hypotheses"))
	if cfg.DreamHours != "" && !validHours(cfg.DreamHours) {
		u.flash(w, r, "error", "Hours must look like 01-06.", "/minds/"+m.Slug+"/dreams")
		return
	}
	if _, err := u.deps.Minds.Update(r.Context(), m.ID, minds.UpdateParams{Config: &cfg}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/dreams")
		return
	}
	u.deps.Brain.Command(r.Context(), m.ID, "reload", nil) //nolint:errcheck
	u.deps.Audit.Log(r.Context(), "dream.settings", "mind", m.ID.String(), map[string]any{"enabled": cfg.DreamingEnabled, "hours": cfg.DreamHours, "interval": cfg.DreamInterval.D().String()})
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "dream.settings", "Operator "+username(r)+" changed dreaming settings", map[string]any{"enabled": cfg.DreamingEnabled, "hours": cfg.DreamHours})
	u.flash(w, r, "ok", "Dreaming settings saved.", "/minds/"+m.Slug+"/dreams")
}

func validHours(h string) bool {
	return dreaming.InWindow(h, time.Now()) || dreaming.InWindow(h, time.Now().Add(12*time.Hour))
}

func (u *UI) dreamPause(w http.ResponseWriter, r *http.Request) {
	paused := r.PostFormValue("paused") == "1"
	dreaming.SetPaused(paused)
	u.deps.Audit.Log(r.Context(), "dream.pause", "system", "", map[string]any{"paused": paused})
	http.Redirect(w, r, r.Referer(), http.StatusSeeOther)
}
