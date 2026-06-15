package auth

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// CookieName is the session cookie.
const CookieName = "noema_session"

// CSRFHeader carries the CSRF token for fetch/XHR requests; forms use the
// hidden field named CSRFField.
const (
	CSRFHeader = "X-CSRF-Token"
	CSRFField  = "_csrf"
)

type principalKey struct{}

// PrincipalFrom returns the authenticated principal, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithPrincipal is exported for tests and internal callers that construct contexts.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Middleware resolves the session cookie or bearer token into a Principal and
// enforces CSRF for cookie sessions on unsafe methods. Unauthenticated requests
// pass through without a principal; route guards decide what to do.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Principal
		var authed bool
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer noema_") {
			if pp, err := s.AuthenticateToken(r.Context(), strings.TrimPrefix(h, "Bearer noema_")); err == nil {
				p, authed = pp, true
			}
		} else if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
			if pp, err := s.Authenticate(r.Context(), c.Value); err == nil {
				p, authed = pp, true
			}
		}
		if authed && p.ViaCookie && !safeMethod(r.Method) {
			if !s.csrfOK(r, p) {
				http.Error(w, "CSRF token missing or invalid", http.StatusForbidden)
				return
			}
		}
		if authed {
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (s *Service) csrfOK(r *http.Request, p Principal) bool {
	// Origin/Referer must match the host when present (defence in depth).
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	} else if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	want, err := s.CSRFToken(r.Context(), p.SessionID)
	if err != nil {
		return false
	}
	got := r.Header.Get(CSRFHeader)
	if got == "" {
		if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.HasPrefix(ct, "multipart/form-data") {
			got = r.PostFormValue(CSRFField)
		}
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// RequireRole returns middleware that rejects requests below min. onFail is
// invoked for unauthenticated or under-privileged requests so the UI can
// redirect to login while the API returns JSON.
func RequireRole(min Role, onFail func(w http.ResponseWriter, r *http.Request, authenticated bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				onFail(w, r, false)
				return
			}
			if !p.User.Role.AtLeast(min) {
				onFail(w, r, true)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP extracts the peer address. Proxy headers are deliberately ignored
// unless the daemon is told it sits behind a trusted proxy; secure default.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// SetSessionCookie writes the session cookie with secure attributes.
func SetSessionCookie(w http.ResponseWriter, token string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

// ClearSessionCookie expires the session cookie.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	SetSessionCookie(w, "", secure, -1)
}
