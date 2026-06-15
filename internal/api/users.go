package api

import (
	"net/http"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/pkg/ids"
)

type userOut struct {
	ID          ids.ID    `json:"id"`
	Username    string    `json:"username"`
	Role        auth.Role `json:"role"`
	Disabled    bool      `json:"disabled"`
	TOTPEnabled bool      `json:"totp_enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

func toUserOut(u auth.User) userOut {
	return userOut{ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled, TOTPEnabled: u.TOTPEnabled, CreatedAt: u.CreatedAt}
}

func (s *Server) usersList(w http.ResponseWriter, r *http.Request) {
	users, err := s.deps.Auth.ListUsers(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]userOut, 0, len(users))
	for _, u := range users {
		out = append(out, toUserOut(u))
	}
	writeJSON(w, http.StatusOK, page[userOut]{Items: out})
}

func (s *Server) usersCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string    `json:"username"`
		Password string    `json:"password"`
		Role     auth.Role `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	u, err := s.deps.Auth.CreateUser(r.Context(), in.Username, in.Password, in.Role)
	if err != nil {
		fail(w, r, wrapInput(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "user.create", "user", u.ID.String(), map[string]any{"username": u.Username, "role": u.Role})
	writeJSON(w, http.StatusCreated, toUserOut(u))
}

func (s *Server) usersPatch(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("invalid id"))
		return
	}
	var in struct {
		Role     *auth.Role `json:"role"`
		Disabled *bool      `json:"disabled"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	if p.User.ID == id && ((in.Disabled != nil && *in.Disabled) || (in.Role != nil && *in.Role != auth.RoleAdministrator)) {
		fail(w, r, ValidationError("you cannot disable or demote your own account"))
		return
	}
	if in.Role != nil {
		if err := s.deps.Auth.SetUserRole(r.Context(), id, *in.Role); err != nil {
			fail(w, r, wrapInput(err))
			return
		}
		s.deps.Audit.Log(r.Context(), "user.role", "user", id.String(), map[string]any{"role": *in.Role})
	}
	if in.Disabled != nil {
		if err := s.deps.Auth.SetUserDisabled(r.Context(), id, *in.Disabled); err != nil {
			fail(w, r, err)
			return
		}
		s.deps.Audit.Log(r.Context(), "user.disabled", "user", id.String(), map[string]any{"disabled": *in.Disabled})
	}
	u, err := s.deps.Auth.GetUser(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserOut(u))
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	writeJSON(w, http.StatusOK, toUserOut(p.User))
}

func (s *Server) mySessions(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	list, err := s.deps.Auth.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	type out struct {
		ID         ids.ID    `json:"id"`
		Current    bool      `json:"current"`
		CreatedAt  time.Time `json:"created_at"`
		LastSeenAt time.Time `json:"last_seen_at"`
		ExpiresAt  time.Time `json:"expires_at"`
		IP         string    `json:"ip"`
		UserAgent  string    `json:"user_agent"`
	}
	items := make([]out, 0, len(list))
	for _, x := range list {
		items = append(items, out{x.ID, x.ID == p.SessionID, x.CreatedAt, x.LastSeenAt, x.ExpiresAt, x.IP, x.UserAgent})
	}
	writeJSON(w, http.StatusOK, page[out]{Items: items})
}

func (s *Server) mySessionRevoke(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("invalid id"))
		return
	}
	if err := s.deps.Auth.RevokeSession(r.Context(), p.User.ID, id); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "session.revoke", "session", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) myTokenCreate(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	var in struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if in.Name == "" {
		fail(w, r, ValidationError("name is required"))
		return
	}
	tok, err := s.deps.Auth.CreateAPIToken(r.Context(), p.User.ID, in.Name, in.ExpiresAt)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "token.create", "user", p.User.ID.String(), map[string]any{"name": in.Name})
	writeJSON(w, http.StatusCreated, map[string]string{"token": tok, "note": "store this now; it is not shown again"})
}

func (s *Server) myTokens(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	list, err := s.deps.Auth.ListAPITokens(r.Context(), p.User.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) myTokenRevoke(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("invalid id"))
		return
	}
	if err := s.deps.Auth.RevokeAPIToken(r.Context(), p.User.ID, id); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "token.revoke", "token", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// wrapInput turns validation-style errors from services into 400s. Services
// return plain errors for bad input; anything wrapping storage sentinels is
// left alone.
func wrapInput(err error) error {
	if err == nil {
		return nil
	}
	if isStorageErr(err) {
		return err
	}
	return ValidationError(err.Error())
}
