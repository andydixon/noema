package api

import (
	_ "embed"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/integrations"
)

// Server composes the API routes, the web UI and the middleware chain.
//
//go:embed openapi.json
var openAPI []byte

type Server struct {
	deps           app.Deps
	mux            *http.ServeMux
	replays        *integrations.ReplayCache
	webhookLimiter *ipBucket
}

// New builds the server. ui registers the HTML routes on the same mux so that
// a single middleware chain (auth, CSRF, headers, limits) covers both.
func New(deps app.Deps, ui interface{ Register(*http.ServeMux) }) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux(), replays: integrations.NewReplayCache(), webhookLimiter: newIPBucket(120, 60)}
	s.routes()
	if ui != nil {
		ui.Register(s.mux)
	}
	return s
}

// Handler returns the fully wrapped root handler.
func (s *Server) Handler() http.Handler {
	return chain(s.mux, s.requestID, s.recoverer, s.securityHeaders, s.bodyLimit, s.logging, s.deps.Auth.Middleware)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.healthz)
	m.HandleFunc("GET /readyz", s.readyz)
	// Metrics need a token with at least observer role; Prometheus supports bearer_token.
	m.Handle("GET /metrics", require(auth.RoleObserver, promhttp.HandlerFor(s.deps.Metrics.Registry, promhttp.HandlerOpts{}).ServeHTTP))

	m.Handle("GET /api/v1/system/status", require(auth.RoleObserver, s.systemStatus))
	m.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAPI) //nolint:errcheck
	})
	m.Handle("GET /api/v1/audit", require(auth.RoleOperator, s.auditList))

	m.Handle("GET /api/v1/users", require(auth.RoleAdministrator, s.usersList))
	m.Handle("POST /api/v1/users", require(auth.RoleAdministrator, s.usersCreate))
	m.Handle("PATCH /api/v1/users/{id}", require(auth.RoleAdministrator, s.usersPatch))
	m.Handle("GET /api/v1/me", require(auth.RoleObserver, s.me))
	m.Handle("GET /api/v1/me/sessions", require(auth.RoleObserver, s.mySessions))
	m.Handle("DELETE /api/v1/me/sessions/{id}", require(auth.RoleObserver, s.mySessionRevoke))
	m.Handle("POST /api/v1/me/tokens", require(auth.RoleObserver, s.myTokenCreate))
	m.Handle("GET /api/v1/me/tokens", require(auth.RoleObserver, s.myTokens))
	m.Handle("DELETE /api/v1/me/tokens/{id}", require(auth.RoleObserver, s.myTokenRevoke))

	s.mindRoutes()
	s.eventRoutes()
	s.workspaceRoutes()
	s.memoryRoutes()
	s.beliefRoutes()
	s.llmRoutes()
	s.promptRoutes()
	s.conversationRoutes()
	s.importRoutes()
	s.motivationRoutes()
	s.cognitionRoutes()
	s.graphRoutes()
	s.capabilityRoutes()
	s.integrationRoutes()
	s.dreamRoutes()
	s.studyRoutes()
	s.assertionRoutes()
	s.privacyRoutes()

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint")
	})
}
