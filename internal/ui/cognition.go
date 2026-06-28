package ui

import (
	"net/http"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/consolidation"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) cognitionRoutes(m *http.ServeMux) {
	m.Handle("GET /notifications", u.require(auth.RoleObserver, u.notificationsPage))
	m.Handle("POST /notifications/{id}/read", u.require(auth.RoleObserver, u.notificationRead))
	m.Handle("POST /notifications/read-all", u.require(auth.RoleObserver, u.notificationsReadAll))
	m.Handle("GET /minds/{mind}/consolidation", u.require(auth.RoleObserver, u.consolidationPage))
	m.Handle("POST /minds/{mind}/consolidation/run", u.require(auth.RoleOperator, u.consolidationRun))
	m.Handle("POST /minds/{mind}/consolidation/reject", u.require(auth.RoleOperator, u.consolidationReject))
	m.Handle("POST /consolidation/pause", u.require(auth.RoleAdministrator, u.consolidationPause))
	m.Handle("POST /minds/{mind}/reflections/run", u.require(auth.RoleOperator, u.reflectionRun))
}

func (u *UI) notificationsPage(w http.ResponseWriter, r *http.Request) {
	list, _ := u.deps.Notifications.List(r.Context(), ids.Nil, r.URL.Query().Get("status"), 200)
	mindsList, _ := u.deps.Minds.List(r.Context(), true)
	names := map[ids.ID]string{}
	for _, m := range mindsList {
		names[m.ID] = m.DisplayName
	}
	u.render(w, r, "notifications", "Notifications", map[string]any{"Items": list, "MindNames": names})
}

func (u *UI) notificationRead(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err == nil {
		u.deps.Notifications.MarkRead(r.Context(), ids.Nil, id) //nolint:errcheck
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

func (u *UI) notificationsReadAll(w http.ResponseWriter, r *http.Request) {
	u.deps.Notifications.MarkAllRead(r.Context(), ids.Nil) //nolint:errcheck
	u.flash(w, r, "ok", "All notifications marked read.", "/notifications")
}

func (u *UI) consolidationPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	jobs, _ := u.deps.Consolidation.Jobs(r.Context(), m.ID, 20)
	refl, _ := u.deps.Reflection.List(r.Context(), m.ID, 5)
	u.render(w, r, "consolidation", m.DisplayName+" · Consolidation", map[string]any{"Mind": m, "Jobs": jobs, "Reflections": refl, "Paused": consolidation.IsPaused()})
}

func (u *UI) consolidationRun(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	job, err := u.consolF(r, m.ID)
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/consolidation")
		return
	}
	u.flash(w, r, "ok", "Consolidation "+job.Status+": "+itoa(job.Produced)+" generalisation(s) from "+itoa(job.Clusters)+" cluster(s).", "/minds/"+m.Slug+"/consolidation")
}

func (u *UI) consolidationReject(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PostFormValue("memory_id"))
	if err != nil {
		u.flash(w, r, "error", "Bad memory id.", "/minds/"+m.Slug+"/consolidation")
		return
	}
	if err := u.deps.Consolidation.Reject(r.Context(), m, id, r.PostFormValue("reason"), username(r)); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/consolidation")
		return
	}
	u.deps.Audit.Log(r.Context(), "consolidation.reject", "memory", id.String(), map[string]any{"reason": r.PostFormValue("reason")})
	u.flash(w, r, "ok", "Generalisation rejected; episodes untouched.", "/minds/"+m.Slug+"/consolidation")
}

func (u *UI) consolidationPause(w http.ResponseWriter, r *http.Request) {
	paused := r.PostFormValue("paused") == "1"
	consolidation.SetPaused(paused)
	u.deps.Audit.Log(r.Context(), "consolidation.pause", "system", "", map[string]any{"paused": paused})
	u.flash(w, r, "ok", "Scheduled consolidation "+map[bool]string{true: "paused", false: "resumed"}[paused]+".", r.Referer())
}

func (u *UI) reflectionRun(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	if _, err := u.reflF(r, m.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/consolidation")
		return
	}
	u.flash(w, r, "ok", "Reflection recorded.", "/minds/"+m.Slug+"/consolidation")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
