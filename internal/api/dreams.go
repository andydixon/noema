package api

import (
	"errors"
	"net/http"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/dreaming"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) dreamRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/dreams", require(auth.RoleObserver, s.dreamsList))
	m.Handle("POST /api/v1/minds/{mind}/dreams/run", require(auth.RoleOperator, s.dreamRun))
	m.Handle("POST /api/v1/minds/{mind}/dreams/settings", require(auth.RoleAdministrator, s.dreamSettings))
	m.Handle("POST /api/v1/minds/{mind}/dreams/hypotheses/{id}/promote", require(auth.RoleOperator, s.dreamReview("promote")))
	m.Handle("POST /api/v1/minds/{mind}/dreams/hypotheses/{id}/reject", require(auth.RoleOperator, s.dreamReview("reject")))
	m.Handle("POST /api/v1/dreams/pause", require(auth.RoleAdministrator, s.dreamPause))
}

func dreamSettingsOf(m minds.Mind) map[string]any {
	c := m.Config
	return map[string]any{"enabled": c.DreamingEnabled, "hours": c.DreamHours, "interval": c.DreamInterval, "llm_calls": c.DreamLLMCalls, "max_memories": c.DreamMaxMemories, "max_hypotheses": c.DreamMaxHypotheses}
}

func (s *Server) dreamsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	dreams, err := s.deps.Dreaming.Dreams(r.Context(), m.ID, 20)
	if err != nil {
		fail(w, r, err)
		return
	}
	hyps, _ := s.deps.Dreaming.Hypotheses(r.Context(), m.ID, r.URL.Query().Get("status"), 100)
	if dreams == nil {
		dreams = []dreaming.Dream{}
	}
	if hyps == nil {
		hyps = []dreaming.Hypothesis{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dreams, "hypotheses": hyps, "paused": dreaming.IsPaused(), "settings": dreamSettingsOf(m)})
}

// RunDream dreams now for a mind; shared with the UI. The brain is put into
// dreaming mode for the duration so the cycle loop yields.
func (s *Server) RunDream(r *http.Request, mindID ids.ID, actorName string) (dreaming.Dream, error) {
	m, err := s.deps.Minds.Get(r.Context(), mindID)
	if err != nil {
		return dreaming.Dream{}, err
	}
	s.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"dreaming": true})    //nolint:errcheck
	defer s.deps.Brain.Command(r.Context(), m.ID, "setmode", map[string]bool{"awake": true}) //nolint:errcheck
	d, err := s.deps.Dreaming.Run(r.Context(), m, "operator", actorName)
	s.deps.Audit.Log(r.Context(), "dream.run", "mind", m.ID.String(), map[string]any{"dream": d.ID, "status": d.Status, "hypotheses": d.Hypotheses})
	return d, err
}

func (s *Server) dreamRun(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	d, err := s.RunDream(r, m.ID, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) dreamSettings(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled       *bool           `json:"enabled"`
		Hours         *string         `json:"hours"`
		Interval      *minds.Duration `json:"interval"`
		LLMCalls      *int            `json:"llm_calls"`
		MaxMemories   *int            `json:"max_memories"`
		MaxHypotheses *int            `json:"max_hypotheses"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	cfg := m.Config
	if in.Enabled != nil {
		cfg.DreamingEnabled = *in.Enabled
	}
	if in.Hours != nil {
		cfg.DreamHours = *in.Hours
	}
	if in.Interval != nil {
		cfg.DreamInterval = *in.Interval
	}
	if in.LLMCalls != nil {
		cfg.DreamLLMCalls = *in.LLMCalls
	}
	if in.MaxMemories != nil {
		cfg.DreamMaxMemories = *in.MaxMemories
	}
	if in.MaxHypotheses != nil {
		cfg.DreamMaxHypotheses = *in.MaxHypotheses
	}
	updated, err := s.deps.Minds.Update(r.Context(), m.ID, minds.UpdateParams{Config: &cfg})
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Brain.Command(r.Context(), m.ID, "reload", nil) //nolint:errcheck
	s.deps.Audit.Log(r.Context(), "dream.settings", "mind", m.ID.String(), dreamSettingsOf(updated))
	s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "dream.settings", "Operator "+actor(r)+" changed dreaming settings", dreamSettingsOf(updated))
	writeJSON(w, http.StatusOK, map[string]any{"settings": dreamSettingsOf(updated)})
}

func (s *Server) dreamReview(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, ok := s.mind(w, r)
		if !ok {
			return
		}
		id, err := ids.Parse(r.PathValue("id"))
		if err != nil {
			fail(w, r, ValidationError("bad hypothesis id"))
			return
		}
		var in struct {
			Note string `json:"note"`
		}
		decode(r, &in) //nolint:errcheck // note is optional
		var h dreaming.Hypothesis
		if action == "promote" {
			h, err = s.deps.Dreaming.Promote(r.Context(), m, id, in.Note, actor(r))
		} else {
			h, err = s.deps.Dreaming.Reject(r.Context(), m, id, in.Note, actor(r))
		}
		if errors.Is(err, dreaming.ErrReviewed) {
			err = ValidationError(err.Error())
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		s.deps.Audit.Log(r.Context(), "dream."+action, "belief", h.BeliefID.String(), map[string]any{"hypothesis": id, "note": in.Note})
		writeJSON(w, http.StatusOK, h)
	}
}

func (s *Server) dreamPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	dreaming.SetPaused(in.Paused)
	s.deps.Audit.Log(r.Context(), "dream.pause", "system", "", map[string]any{"paused": in.Paused})
	writeJSON(w, http.StatusOK, map[string]any{"paused": in.Paused})
}
