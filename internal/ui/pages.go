package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) loginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.PrincipalFrom(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if n, err := u.deps.Auth.CountUsers(r.Context()); err == nil && n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	u.render(w, r, "login", "Sign in", map[string]any{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (u *UI) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		u.errorPage(w, r, http.StatusBadRequest, "Bad form.")
		return
	}
	ip := auth.ClientIP(r, false)
	sess, token, err := u.deps.Auth.Login(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"), ip, r.UserAgent())
	if err != nil {
		u.deps.Metrics.AuthFailures.Inc()
		msg := "Invalid username or password."
		switch {
		case errors.Is(err, auth.ErrLocked):
			msg = "Account temporarily locked after repeated failures. Try again later."
		case errors.Is(err, auth.ErrRateLimited):
			msg = "Too many attempts from this address. Slow down."
		case errors.Is(err, auth.ErrDisabled):
			msg = "This account is disabled."
		}
		u.deps.Audit.Log(r.Context(), "login.failed", "user", "", map[string]any{"username": r.PostFormValue("username"), "reason": err.Error()})
		w.WriteHeader(http.StatusUnauthorized)
		u.render(w, r, "login", "Sign in", map[string]any{"Error": msg, "Next": safeNext(r.PostFormValue("next"))})
		return
	}
	auth.SetSessionCookie(w, token, u.deps.Config.Production(), int(u.deps.Config.SessionAbsoluteTTL.Seconds()))
	ctx := auth.WithPrincipal(r.Context(), auth.Principal{User: auth.User{ID: sess.UserID, Username: r.PostFormValue("username")}, SessionID: sess.ID})
	u.deps.Audit.Log(ctx, "login.success", "session", sess.ID.String(), nil)
	to := safeNext(r.PostFormValue("next"))
	if to == "" {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (u *UI) logout(w http.ResponseWriter, r *http.Request) {
	if p, ok := auth.PrincipalFrom(r.Context()); ok && !p.SessionID.IsZero() {
		u.deps.Auth.Logout(r.Context(), p.SessionID) //nolint:errcheck
		u.deps.Audit.Log(r.Context(), "logout", "session", p.SessionID.String(), nil)
	}
	auth.ClearSessionCookie(w, u.deps.Config.Production())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// setup is only reachable while no users exist; it is the browser equivalent
// of the bootstrap environment variables.
func (u *UI) setupForm(w http.ResponseWriter, r *http.Request) {
	if n, err := u.deps.Auth.CountUsers(r.Context()); err != nil || n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	u.render(w, r, "setup", "Setup", nil)
}

func (u *UI) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if n, err := u.deps.Auth.CountUsers(r.Context()); err != nil || n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.ParseForm() //nolint:errcheck
	if r.PostFormValue("password") != r.PostFormValue("confirm") {
		u.render(w, r, "setup", "Setup", map[string]any{"Error": "Passwords do not match."})
		return
	}
	user, err := u.deps.Auth.CreateUser(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"), auth.RoleAdministrator)
	if err != nil {
		u.render(w, r, "setup", "Setup", map[string]any{"Error": err.Error()})
		return
	}
	u.deps.Audit.Log(r.Context(), "user.bootstrap", "user", user.ID.String(), map[string]any{"username": user.Username, "via": "setup page"})
	u.flash(w, r, "ok", "Administrator created. Sign in.", "/login")
}

func (u *UI) dashboard(w http.ResponseWriter, r *http.Request) {
	entries, _ := u.deps.Audit.List(r.Context(), 0, 8, "", "")
	u.render(w, r, "dashboard", "Dashboard", map[string]any{"Minds": 0, "Status": u.statusF(r), "Audit": entries})
}

func (u *UI) system(w http.ResponseWriter, r *http.Request) {
	u.render(w, r, "system", "System", u.statusF(r))
}

func (u *UI) audit(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	entries, err := u.deps.Audit.List(r.Context(), before, 100, r.URL.Query().Get("target_kind"), r.URL.Query().Get("target_id"))
	if err != nil {
		u.errorPage(w, r, 500, "Could not load audit log.")
		return
	}
	var next int64
	if len(entries) == 100 {
		next = entries[len(entries)-1].Seq
	}
	u.render(w, r, "audit", "Audit", map[string]any{"Entries": entries, "Next": next})
}

func (u *UI) account(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	sessions, _ := u.deps.Auth.ListSessions(r.Context(), p.User.ID)
	tokens, _ := u.deps.Auth.ListAPITokens(r.Context(), p.User.ID)
	var newToken string
	if c, err := r.Cookie("noema_newtoken"); err == nil {
		newToken = c.Value
		http.SetCookie(w, &http.Cookie{Name: "noema_newtoken", Path: "/account", MaxAge: -1, HttpOnly: true})
	}
	u.render(w, r, "account", "Account", map[string]any{"Sessions": sessions, "Tokens": tokens, "NewToken": newToken})
}

func (u *UI) accountPassword(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	r.ParseForm() //nolint:errcheck
	if r.PostFormValue("password") != r.PostFormValue("confirm") {
		u.flash(w, r, "error", "New passwords do not match.", "/account")
		return
	}
	// Re-verify the current password before changing it.
	if _, _, err := u.deps.Auth.Login(r.Context(), p.User.Username, r.PostFormValue("current"), auth.ClientIP(r, false), "password-change"); err != nil {
		u.flash(w, r, "error", "Current password incorrect.", "/account")
		return
	}
	if err := u.deps.Auth.ChangePassword(r.Context(), p.User.ID, r.PostFormValue("password"), p.SessionID); err != nil {
		u.flash(w, r, "error", err.Error(), "/account")
		return
	}
	u.deps.Audit.Log(r.Context(), "user.password_change", "user", p.User.ID.String(), nil)
	u.flash(w, r, "ok", "Password changed. Other sessions were signed out.", "/account")
}

func (u *UI) accountSessionRevoke(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil || u.deps.Auth.RevokeSession(r.Context(), p.User.ID, id) != nil {
		u.flash(w, r, "error", "Session not found.", "/account")
		return
	}
	u.deps.Audit.Log(r.Context(), "session.revoke", "session", id.String(), nil)
	u.flash(w, r, "ok", "Session revoked.", "/account")
}

func (u *UI) accountTokenCreate(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	r.ParseForm() //nolint:errcheck
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		u.flash(w, r, "error", "Token name required.", "/account")
		return
	}
	tok, err := u.deps.Auth.CreateAPIToken(r.Context(), p.User.ID, name, nil)
	if err != nil {
		u.flash(w, r, "error", "Could not create token.", "/account")
		return
	}
	u.deps.Audit.Log(r.Context(), "token.create", "user", p.User.ID.String(), map[string]any{"name": name})
	// Shown exactly once on the next page load, then the cookie is cleared.
	http.SetCookie(w, &http.Cookie{Name: "noema_newtoken", Value: tok, Path: "/account", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.deps.Config.Production()})
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (u *UI) accountTokenRevoke(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil || u.deps.Auth.RevokeAPIToken(r.Context(), p.User.ID, id) != nil {
		u.flash(w, r, "error", "Token not found.", "/account")
		return
	}
	u.deps.Audit.Log(r.Context(), "token.revoke", "token", id.String(), nil)
	u.flash(w, r, "ok", "Token revoked.", "/account")
}

func (u *UI) users(w http.ResponseWriter, r *http.Request) {
	users, err := u.deps.Auth.ListUsers(r.Context())
	if err != nil {
		u.errorPage(w, r, 500, "Could not load users.")
		return
	}
	u.render(w, r, "users", "Users", map[string]any{"Users": users})
}

func (u *UI) usersCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	user, err := u.deps.Auth.CreateUser(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"), auth.Role(r.PostFormValue("role")))
	if err != nil {
		msg := err.Error()
		if errors.Is(err, storage.ErrConflict) {
			msg = "That username already exists."
		}
		u.flash(w, r, "error", msg, "/admin/users")
		return
	}
	u.deps.Audit.Log(r.Context(), "user.create", "user", user.ID.String(), map[string]any{"username": user.Username, "role": user.Role})
	u.flash(w, r, "ok", "User created.", "/admin/users")
}

func (u *UI) usersUpdate(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.flash(w, r, "error", "Invalid user.", "/admin/users")
		return
	}
	if id == p.User.ID {
		u.flash(w, r, "error", "You cannot change your own account here.", "/admin/users")
		return
	}
	r.ParseForm() //nolint:errcheck
	switch r.PostFormValue("action") {
	case "role":
		role := auth.Role(r.PostFormValue("role"))
		if err := u.deps.Auth.SetUserRole(r.Context(), id, role); err != nil {
			u.flash(w, r, "error", err.Error(), "/admin/users")
			return
		}
		u.deps.Audit.Log(r.Context(), "user.role", "user", id.String(), map[string]any{"role": role})
	case "disable", "enable":
		disabled := r.PostFormValue("action") == "disable"
		if err := u.deps.Auth.SetUserDisabled(r.Context(), id, disabled); err != nil {
			u.flash(w, r, "error", err.Error(), "/admin/users")
			return
		}
		u.deps.Audit.Log(r.Context(), "user.disabled", "user", id.String(), map[string]any{"disabled": disabled})
	default:
		u.flash(w, r, "error", "Unknown action.", "/admin/users")
		return
	}
	u.flash(w, r, "ok", "User updated.", "/admin/users")
}

// safeNext only allows same-site relative paths for post-login redirects.
func safeNext(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return ""
	}
	if _, err := url.Parse(s); err != nil {
		return ""
	}
	return s
}
