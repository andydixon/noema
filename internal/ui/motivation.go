package ui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) motivationRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/goals", u.require(auth.RoleObserver, u.goalsPage))
	m.Handle("POST /minds/{mind}/goals", u.require(auth.RoleOperator, u.goalCreate))
	m.Handle("POST /minds/{mind}/goals/{id}", u.require(auth.RoleOperator, u.goalUpdate))
	m.Handle("GET /minds/{mind}/predictions", u.require(auth.RoleObserver, u.predictionsPage))
	m.Handle("POST /minds/{mind}/predictions", u.require(auth.RoleOperator, u.predictionCreate))
	m.Handle("POST /minds/{mind}/predictions/{id}/resolve", u.require(auth.RoleOperator, u.predictionResolve))
	m.Handle("POST /minds/{mind}/state/reset", u.require(auth.RoleOperator, u.stateReset))
}

func (u *UI) goalsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Goals.List(r.Context(), m.ID, nil, 0)
	st, _ := u.deps.Goals.Stats(r.Context(), m.ID)
	u.render(w, r, "goals", m.DisplayName+" · Goals", map[string]any{"Mind": m, "Goals": list, "Stats": st})
}

func (u *UI) goalCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	prio, _ := strconv.ParseFloat(r.PostFormValue("priority"), 64)
	var parent ids.ID
	if v := r.PostFormValue("parent_id"); v != "" {
		parent, _ = ids.Parse(v)
	}
	if _, err := u.goalF(r, goals.New{MindID: m.ID, ParentID: parent, Title: r.PostFormValue("title"), Description: r.PostFormValue("description"), Kind: goals.Kind(r.PostFormValue("kind")), Priority: prio, Subject: r.PostFormValue("subject")}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/goals")
		return
	}
	u.flash(w, r, "ok", "Goal created.", "/minds/"+m.Slug+"/goals")
}

func (u *UI) goalUpdate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.flash(w, r, "error", "Bad goal.", "/minds/"+m.Slug+"/goals")
		return
	}
	r.ParseForm() //nolint:errcheck
	var up goals.Update
	switch r.PostFormValue("action") {
	case "progress":
		p, _ := strconv.ParseFloat(r.PostFormValue("progress"), 64)
		up.Progress = &p
	case "complete":
		st := goals.StatusCompleted
		up.Status = &st
	case "abandon":
		st := goals.StatusAbandoned
		up.Status = &st
	}
	if _, err := u.goalUpdF(r, m.ID, id, up); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/goals")
		return
	}
	u.flash(w, r, "ok", "Goal updated.", "/minds/"+m.Slug+"/goals")
}

func (u *UI) predictionsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Predictions.List(r.Context(), m.ID, nil, 200)
	stats, _ := u.deps.Predictions.Stats(r.Context(), m.ID)
	cal, _ := u.deps.Predictions.Calibrate(r.Context(), m.ID, time.Now().Add(-90*24*time.Hour))
	u.render(w, r, "predictions", m.DisplayName+" · Predictions", map[string]any{"Mind": m, "Predictions": list, "Stats": stats, "Calibration": cal})
}

func (u *UI) predictionCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	conf, _ := strconv.ParseFloat(r.PostFormValue("confidence"), 64)
	hours, _ := strconv.Atoi(r.PostFormValue("hours"))
	if hours <= 0 {
		hours = 24
	}
	var terms []string
	for _, t := range strings.Split(r.PostFormValue("terms"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			terms = append(terms, t)
		}
	}
	crit := predictions.Criteria{Kind: r.PostFormValue("kind"), Expect: r.PostFormValue("expect"), Terms: terms}
	if _, err := u.predF(r, predictions.New{MindID: m.ID, Statement: r.PostFormValue("statement"), Confidence: conf, WindowEnd: time.Now().Add(time.Duration(hours) * time.Hour), Criteria: crit}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/predictions")
		return
	}
	u.flash(w, r, "ok", "Prediction recorded.", "/minds/"+m.Slug+"/predictions")
}

func (u *UI) predictionResolve(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.flash(w, r, "error", "Bad prediction.", "/minds/"+m.Slug+"/predictions")
		return
	}
	if _, err := u.resolveF(r, m.ID, id, predictions.Status(r.PostFormValue("outcome")), "resolved in the UI"); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/predictions")
		return
	}
	u.flash(w, r, "ok", "Prediction resolved.", "/minds/"+m.Slug+"/predictions")
}

func (u *UI) stateReset(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	if _, err := u.resetF(r, m.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug)
		return
	}
	u.flash(w, r, "ok", "Drives and affect reset to baseline.", "/minds/"+m.Slug)
}
