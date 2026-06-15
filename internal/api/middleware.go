package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/observability"
)

type requestIDKey struct{}

// RequestID returns the id assigned to the request.
func RequestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

func logger(r *http.Request) *slog.Logger { return observability.Logger(r.Context()) }

// chain applies middleware in the order given (first is outermost).
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		rand.Read(b[:]) //nolint:errcheck
		id := hex.EncodeToString(b[:])
		ip := auth.ClientIP(r, false)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = observability.WithLogger(ctx, s.deps.Log.With("request_id", id))
		ctx = audit.WithIP(ctx, ip)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}

// Flush lets SSE handlers flush through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		d := time.Since(start)
		s.deps.Metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		s.deps.Metrics.HTTPDuration.WithLabelValues(route).Observe(d.Seconds())
		logger(r).Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", d.Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logger(r).Error("handler panicked", "panic", rec, "stack", string(debug.Stack()))
				writeError(w, r, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets a strict CSP (no inline scripts, self only) and the
// usual hardening headers (T10).
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	csp := "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.deps.Config.Production() {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, s.deps.Config.MaxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// apiAuthFail is the RequireRole failure handler for JSON routes.
func apiAuthFail(w http.ResponseWriter, r *http.Request, authenticated bool) {
	if authenticated {
		writeError(w, r, http.StatusForbidden, "forbidden", "insufficient role")
		return
	}
	writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required")
}

// require wraps a handler with a role check for API routes.
func require(min auth.Role, h http.HandlerFunc) http.Handler {
	return auth.RequireRole(min, apiAuthFail)(h)
}
