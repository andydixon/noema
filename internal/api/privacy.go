package api

import (
	"errors"
	"net/http"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/privacy"
	"github.com/andydixon/noema/pkg/ids"
)

// privacyRoutes: the audited redaction path. Administrators only; every
// call is irreversible by design and says so in its response.
func (s *Server) privacyRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/redactions", require(auth.RoleOperator, s.redactionsList))
	m.Handle("POST /api/v1/minds/{mind}/redactions", require(auth.RoleAdministrator, s.redactionCreate))
}

func (s *Server) redactionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Privacy.List(r.Context(), m.ID, 200)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []privacy.Redaction{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// RedactionInput is the request body (API and UI form).
type RedactionInput struct {
	Kind    string `json:"kind"`
	ID      ids.ID `json:"id"`
	Reason  string `json:"reason"`
	Policy  string `json:"policy"`
	Cascade bool   `json:"cascade"`
}

// Redact performs a redaction; shared with the UI.
func (s *Server) Redact(r *http.Request, mind ids.ID, in RedactionInput, by string) (privacy.Redaction, []privacy.Redaction, error) {
	p, c, err := s.deps.Privacy.Redact(r.Context(), privacy.Request{MindID: mind, Kind: privacy.Kind(in.Kind), ID: in.ID, Reason: in.Reason, Policy: in.Policy, Cascade: in.Cascade, By: by})
	switch {
	case errors.Is(err, privacy.ErrInvalid):
		return p, c, ValidationError(err.Error())
	case errors.Is(err, privacy.ErrAlreadyRedacted):
		return p, c, ValidationError("already redacted")
	}
	return p, c, err
}

func (s *Server) redactionCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in RedactionInput
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	p, c, err := s.Redact(r, m.ID, in, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"redaction": p, "cascaded": c, "note": "content is irrecoverable; the row, its links and a hash of the removed content remain"})
}
