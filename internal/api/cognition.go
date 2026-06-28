package api

import (
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/consolidation"
	"github.com/andydixon/noema/internal/notifications"
	"github.com/andydixon/noema/internal/reflection"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) cognitionRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/narrative", require(auth.RoleObserver, s.narrativeGet))
	m.Handle("GET /api/v1/minds/{mind}/consolidation", require(auth.RoleObserver, s.consolidationJobs))
	m.Handle("POST /api/v1/minds/{mind}/consolidation/run", require(auth.RoleOperator, s.consolidationRun))
	m.Handle("POST /api/v1/minds/{mind}/consolidation/reject", require(auth.RoleOperator, s.consolidationReject))
	m.Handle("POST /api/v1/consolidation/pause", require(auth.RoleAdministrator, s.consolidationPause))
	m.Handle("GET /api/v1/minds/{mind}/reflections", require(auth.RoleObserver, s.reflectionsList))
	m.Handle("POST /api/v1/minds/{mind}/reflections/run", require(auth.RoleOperator, s.reflectionRun))
	m.Handle("GET /api/v1/notifications", require(auth.RoleObserver, s.notificationsList))
	m.Handle("GET /api/v1/minds/{mind}/notifications", require(auth.RoleObserver, s.notificationsList))
	m.Handle("POST /api/v1/notifications/{id}/read", require(auth.RoleObserver, s.notificationRead))
	m.Handle("POST /api/v1/notifications/read-all", require(auth.RoleObserver, s.notificationsReadAll))
}

func (s *Server) narrativeGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	n, err := s.deps.Narrative.Get(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) consolidationJobs(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	jobs, err := s.deps.Consolidation.Jobs(r.Context(), m.ID, 20)
	if err != nil {
		fail(w, r, err)
		return
	}
	if jobs == nil {
		jobs = []consolidation.Job{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": jobs, "paused": consolidation.IsPaused(), "settings": map[string]any{"enabled": m.Config.ConsolidationEnabled, "interval": m.Config.ConsolidationInterval, "min_episodes": m.Config.ConsolidationMinEpisodes, "llm_calls": m.Config.ConsolidationLLMCalls}})
}

// RunConsolidation runs a job now for a mind; shared with the UI.
func (s *Server) RunConsolidation(r *http.Request, mindID ids.ID) (consolidation.Job, error) {
	m, err := s.deps.Minds.Get(r.Context(), mindID)
	if err != nil {
		return consolidation.Job{}, err
	}
	s.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"consolidating": true}) //nolint:errcheck
	defer s.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"awake": true})   //nolint:errcheck
	job, err := s.deps.Consolidation.Run(r.Context(), m, "operator", actor(r))
	s.deps.Audit.Log(r.Context(), "consolidation.run", "mind", m.ID.String(), map[string]any{"job": job.ID, "status": job.Status, "produced": job.Produced})
	return job, err
}

func (s *Server) consolidationRun(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	job, err := s.RunConsolidation(r, m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) consolidationReject(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		MemoryID ids.ID `json:"memory_id"`
		Reason   string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.deps.Consolidation.Reject(r.Context(), m, in.MemoryID, in.Reason, actor(r)); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "consolidation.reject", "memory", in.MemoryID.String(), map[string]any{"reason": in.Reason})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) consolidationPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	consolidation.SetPaused(in.Paused)
	s.deps.Audit.Log(r.Context(), "consolidation.pause", "system", "", map[string]any{"paused": in.Paused})
	writeJSON(w, http.StatusOK, map[string]bool{"paused": in.Paused})
}

func (s *Server) reflectionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Reflection.List(r.Context(), m.ID, 20)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []reflection.Reflection{}
	}
	writeJSON(w, http.StatusOK, page[reflection.Reflection]{Items: list})
}

// RunReflection runs a reflection now; shared with the UI.
func (s *Server) RunReflection(r *http.Request, mindID ids.ID) (reflection.Reflection, error) {
	m, err := s.deps.Minds.Get(r.Context(), mindID)
	if err != nil {
		return reflection.Reflection{}, err
	}
	s.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"reflecting": true})  //nolint:errcheck
	defer s.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"awake": true}) //nolint:errcheck
	ref, err := s.deps.Reflection.Run(r.Context(), m, "operator", actor(r))
	s.deps.Audit.Log(r.Context(), "reflection.run", "mind", m.ID.String(), map[string]any{"reflection": ref.ID})
	return ref, err
}

func (s *Server) reflectionRun(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	ref, err := s.RunReflection(r, m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) notificationsList(w http.ResponseWriter, r *http.Request) {
	var mind ids.ID
	if r.PathValue("mind") != "" {
		m, ok := s.mind(w, r)
		if !ok {
			return
		}
		mind = m.ID
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.deps.Notifications.List(r.Context(), mind, r.URL.Query().Get("status"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []notifications.Notification{}
	}
	unread, _ := s.deps.Notifications.CountNew(r.Context(), mind)
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "unread": unread})
}

func (s *Server) notificationRead(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	if err := s.deps.Notifications.MarkRead(r.Context(), ids.Nil, id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notificationsReadAll(w http.ResponseWriter, r *http.Request) {
	n, err := s.deps.Notifications.MarkAllRead(r.Context(), ids.Nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"marked": n})
}

var _ = events.NotificationCreated
