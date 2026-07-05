package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/andydixon/noema/internal/auth"
	evstore "github.com/andydixon/noema/internal/events"
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
	if errors.Is(err, auth.ErrSecondFactorRequired) {
		// Password accepted; the pending token lives in a short cookie scoped to the second step.
		http.SetCookie(w, &http.Cookie{Name: "noema_2fa", Value: token, Path: "/login/totp", MaxAge: 300, HttpOnly: true, Secure: u.deps.Config.Production(), SameSite: http.SameSiteStrictMode}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
		u.render(w, r, "login_totp", "Second factor", map[string]any{"Next": safeNext(r.PostFormValue("next"))})
		return
	}
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
	http.Redirect(w, r, to, http.StatusSeeOther) // #nosec G710 -- target is a validated same-origin path (safeNext / fixed prefix)
}

// loginTOTP completes a two-step login with a TOTP or recovery code.
func (u *UI) loginTOTP(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("noema_2fa")
	if err != nil || r.ParseForm() != nil {
		u.flash(w, r, "error", "Sign in again; the second step expired.", "/login")
		return
	}
	ip := auth.ClientIP(r, false)
	sess, token, usedRecovery, err := u.deps.Auth.CompleteLogin(r.Context(), c.Value, r.PostFormValue("code"), ip, r.UserAgent())
	if err != nil {
		u.deps.Metrics.AuthFailures.Inc()
		u.deps.Audit.Log(r.Context(), "login.failed", "user", "", map[string]any{"reason": "second factor: " + err.Error()})
		msg := "That code was not accepted."
		if errors.Is(err, auth.ErrRateLimited) {
			msg = "Too many attempts from this address. Slow down."
		}
		w.WriteHeader(http.StatusUnauthorized)
		u.render(w, r, "login_totp", "Second factor", map[string]any{"Error": msg, "Next": safeNext(r.PostFormValue("next"))})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "noema_2fa", Path: "/login/totp", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.deps.Config.Production()}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
	auth.SetSessionCookie(w, token, u.deps.Config.Production(), int(u.deps.Config.SessionAbsoluteTTL.Seconds()))
	ctx := auth.WithPrincipal(r.Context(), auth.Principal{User: auth.User{ID: sess.UserID}, SessionID: sess.ID})
	u.deps.Audit.Log(ctx, "login.success", "session", sess.ID.String(), map[string]any{"second_factor": true, "recovery_code": usedRecovery})
	to := safeNext(r.PostFormValue("next"))
	if to == "" {
		to = "/"
	}
	if usedRecovery {
		to = "/account"
	}
	http.Redirect(w, r, to, http.StatusSeeOther) // #nosec G710 -- target is a validated same-origin path (safeNext / fixed prefix)
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
	mindsList, _ := u.deps.Minds.List(r.Context(), false)
	names := map[ids.ID]string{}
	for _, m := range mindsList {
		names[m.ID] = m.DisplayName
	}
	evs, _ := u.deps.Events.Query(r.Context(), evstore.Filter{MinSalience: 0.6, Limit: 12})
	pending := 0
	if u.deps.Capabilities != nil {
		if list, err := u.deps.Capabilities.Approvals(r.Context(), ids.Nil, "pending", 500); err == nil {
			pending = len(list)
		}
	}
	u.render(w, r, "dashboard", "Dashboard", map[string]any{"Minds": mindsList, "MindNames": names, "Events": evs, "Status": u.statusF(r), "Audit": entries, "Pending": pending})
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
		http.SetCookie(w, &http.Cookie{Name: "noema_newtoken", Path: "/account", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.deps.Config.Production()}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
	}
	me, _ := u.deps.Auth.GetUser(r.Context(), p.User.ID)
	data := map[string]any{"Sessions": sessions, "Tokens": tokens, "NewToken": newToken, "TOTPEnabled": me.TOTPEnabled, "TOTPAvailable": u.deps.Secrets != nil && u.deps.Secrets.CanEncrypt()}
	if me.TOTPEnabled {
		data["RecoveryLeft"] = u.deps.Auth.RecoveryCodesLeft(r.Context(), p.User.ID)
	}
	if c, err := r.Cookie("noema_totp_setup"); err == nil {
		// Enrolment in progress: the cookie only marks the state; the secret is re-derived server-side.
		http.SetCookie(w, &http.Cookie{Name: "noema_totp_setup", Path: "/account", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.deps.Config.Production()}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
		data["Enrolling"] = c.Value == "1"
		if secret, uri, err := u.deps.Auth.PendingTOTP(r.Context(), p.User.ID, "Noema"); err == nil {
			data["TOTPSecret"], data["TOTPURI"] = secret, uri
		}
	}
	if c, err := r.Cookie("noema_recovery"); err == nil {
		http.SetCookie(w, &http.Cookie{Name: "noema_recovery", Path: "/account", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.deps.Config.Production()}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
		data["RecoveryCodes"] = strings.Split(c.Value, ".")
	}
	u.render(w, r, "account", "Account", data)
}

func (u *UI) accountTOTPBegin(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	if _, _, err := u.deps.Auth.BeginTOTP(r.Context(), p.User.ID, "Noema"); err != nil {
		u.flash(w, r, "error", err.Error(), "/account")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "noema_totp_setup", Value: "1", Path: "/account", MaxAge: 600, HttpOnly: true, Secure: u.deps.Config.Production(), SameSite: http.SameSiteStrictMode}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
	http.Redirect(w, r, "/account#totp", http.StatusSeeOther)
}

func (u *UI) accountTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	codes, err := u.deps.Auth.ConfirmTOTP(r.Context(), p.User.ID, r.PostFormValue("code"))
	if err != nil {
		http.SetCookie(w, &http.Cookie{Name: "noema_totp_setup", Value: "1", Path: "/account", MaxAge: 600, HttpOnly: true, Secure: u.deps.Config.Production(), SameSite: http.SameSiteStrictMode}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
		u.flash(w, r, "error", "Code not accepted; try the next one from your app.", "/account#totp")
		return
	}
	u.deps.Audit.Log(r.Context(), "user.totp.enabled", "user", p.User.ID.String(), nil)
	http.SetCookie(w, &http.Cookie{Name: "noema_recovery", Value: strings.Join(codes, "."), Path: "/account", MaxAge: 300, HttpOnly: true, Secure: u.deps.Config.Production(), SameSite: http.SameSiteStrictMode}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
	http.Redirect(w, r, "/account#totp", http.StatusSeeOther)
}

func (u *UI) accountTOTPDisable(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	ip := auth.ClientIP(r, false)
	if _, _, err := u.deps.Auth.Login(r.Context(), p.User.Username, r.PostFormValue("current"), ip, r.UserAgent()); err != nil && !errors.Is(err, auth.ErrSecondFactorRequired) {
		u.flash(w, r, "error", "Current password incorrect.", "/account#totp")
		return
	}
	if err := u.deps.Auth.DisableTOTP(r.Context(), p.User.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/account#totp")
		return
	}
	u.deps.Audit.Log(r.Context(), "user.totp.disabled", "user", p.User.ID.String(), nil)
	u.flash(w, r, "ok", "Two-factor authentication disabled.", "/account#totp")
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
	http.SetCookie(w, &http.Cookie{Name: "noema_newtoken", Value: tok, Path: "/account", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.deps.Config.Production()}) // #nosec G124 -- Secure follows NOEMA_ENV=production; development runs over plain HTTP
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
	case "reset-totp":
		if err := u.deps.Auth.DisableTOTP(r.Context(), id); err != nil {
			u.flash(w, r, "error", err.Error(), "/admin/users")
			return
		}
		u.deps.Audit.Log(r.Context(), "user.totp.reset", "user", id.String(), nil)
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
