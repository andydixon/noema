package api

import (
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/epistemics"
	"github.com/andydixon/noema/pkg/ids"
)

// assertionRoutes expose who asserted what to a mind, kept apart from what
// the mind believes.
func (s *Server) assertionRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/assertions", require(auth.RoleObserver, s.assertionsList))
	m.Handle("GET /api/v1/minds/{mind}/assertions/actors", require(auth.RoleObserver, s.assertionActors))
}

func (s *Server) assertionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := epistemics.Filter{AttributedTo: q.Get("actor"), Stance: q.Get("stance"), Limit: intParam(q.Get("limit"), 100)}
	if b := q.Get("belief"); b != "" {
		id, err := ids.Parse(b)
		if err != nil {
			fail(w, r, ValidationError("bad belief id"))
			return
		}
		f.BeliefID = id
	}
	list, err := s.deps.Assertions.List(r.Context(), m.ID, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []epistemics.Assertion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) assertionActors(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Assertions.Actors(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []epistemics.ActorSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// intParam parses a bounded positive integer query value.
func intParam(v string, def int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
