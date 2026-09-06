// Package harness assembles the daemon the way cmd/noemad does, with a
// deterministic cognitive registry and no model, for end-to-end tests
// (security, epistemics, milestones).
package harness

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/cognition"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/consolidation"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/dreaming"
	"github.com/andydixon/noema/internal/epistemics"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/integrations"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/narrative"
	"github.com/andydixon/noema/internal/notifications"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/privacy"
	"github.com/andydixon/noema/internal/reflection"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/ui"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

type Harness struct {
	T     *testing.T
	Deps  app.Deps
	Srv   *httptest.Server
	Admin string // bearer token, administrator
	Obs   string // bearer token, observer
}

const AdminPass, ObserverPass = "administrator-pass-1", "observer-pass-12345"

// Options adjust the assembled daemon for a test.
type Options struct {
	// Capabilities are extra test capabilities registered before the
	// registry is frozen.
	Capabilities []capabilities.Capability
}

// Start builds and serves a daemon for one test.
func Start(t *testing.T) *Harness {
	t.Helper()
	return StartWith(t, Options{})
}

// StartWith builds a daemon with options.
func StartWith(t *testing.T, o Options) *Harness {
	t.Helper()
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	cfg := config.Defaults()
	cfg.DatabaseURL = "postgres://test"
	sec, _ := secrets.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	llmStore := router.NewStore(pool)
	authSvc := auth.NewService(pool, time.Hour, 2*time.Hour)
	authSvc.SetSecrets(sec)
	deps := app.Deps{Config: cfg, Log: log, DB: pool, Auth: authSvc, Audit: audit.New(pool, log), Metrics: observability.NewMetrics(),
		Minds: minds.NewStore(pool), Events: evstore.NewStore(pool, evstore.NewBus(64), nil), Memory: memory.NewStore(ctx, pool, nil), Beliefs: beliefs.NewStore(pool),
		Conversations: conversation.NewStore(pool), Goals: goals.NewStore(pool), Motivation: motivation.NewStore(pool), Predictions: predictions.NewStore(pool), Secrets: sec, LLMStore: llmStore, LLM: router.NewRouter(llmStore, sec, log, nil, 2), Prompts: prompts.NewStore(pool), Migrations: migrations.FS, Started: time.Now()}
	deps.Resolver = &predictions.Resolver{Store: deps.Predictions, Events: deps.Events, Beliefs: deps.Beliefs, Log: log}
	deps.Graph = graph.NewStore(pool)
	capReg := capabilities.NewRegistry()
	for _, c := range capabilities.Builtins() {
		capReg.Add(c)
	}
	for _, c := range o.Capabilities {
		capReg.Add(c)
	}
	capReg.Freeze()
	deps.Capabilities = &capabilities.Service{Registry: capReg, DB: pool, Events: deps.Events, Log: log}
	deps.Integrations = &integrations.Manager{Store: integrations.NewStore(pool), Events: deps.Events, Graph: deps.Graph, Log: log, Pollers: map[integrations.Kind]integrations.Poller{}}
	deps.Dreaming = &dreaming.Service{DB: pool, Memory: deps.Memory, Beliefs: deps.Beliefs, Events: deps.Events, Log: log}
	deps.Snapshots = &snapshots.Store{DB: pool, Events: deps.Events, Minds: deps.Minds}
	deps.Assertions = epistemics.NewStore(pool)
	deps.Privacy = &privacy.Service{DB: pool, Events: deps.Events, Audit: deps.Audit, Snapshots: deps.Snapshots, Log: log}
	deps.Narrative = narrative.NewStore(pool)
	deps.Notifications = notifications.NewStore(pool)
	deps.Consolidation = &consolidation.Service{DB: pool, Memory: deps.Memory, Beliefs: deps.Beliefs, Events: deps.Events, Minds: deps.Minds, Log: log}
	deps.Reflection = &reflection.Service{DB: pool, Events: deps.Events, Beliefs: deps.Beliefs, Goals: deps.Goals, Memory: deps.Memory, Predictions: deps.Predictions, Log: log}
	if _, err := deps.Prompts.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	group := scheduler.NewGroup(ctx, log)
	t.Cleanup(func() { group.Stop() })
	registry := cognition.NewRegistry(cognition.Stores{Memory: deps.Memory, Beliefs: deps.Beliefs, Conversations: deps.Conversations, Graph: deps.Graph, Assertions: deps.Assertions})
	deps.Brain = brain.NewManager(brain.Config{DB: pool, Events: deps.Events, Minds: deps.Minds, Registry: registry, Pool: scheduler.NewPool(2), Log: log, Metrics: deps.Metrics,
		Services: brain.Services{Events: deps.Events, Memory: deps.Memory, Beliefs: deps.Beliefs, LLM: deps.LLM, Prompts: deps.Prompts, Goals: deps.Goals, Motivation: deps.Motivation, Predictions: deps.Predictions, Capabilities: deps.Capabilities}}, group)
	if err := deps.Brain.Start(ctx); err != nil {
		t.Fatal(err)
	}
	adminUser, err := deps.Auth.CreateUser(ctx, "admin", AdminPass, auth.RoleAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	obsUser, _ := deps.Auth.CreateUser(ctx, "watcher", ObserverPass, auth.RoleObserver)
	adminTok, _ := deps.Auth.CreateAPIToken(ctx, adminUser.ID, "t", nil)
	obsTok, _ := deps.Auth.CreateAPIToken(ctx, obsUser.ID, "t", nil)
	var srv *api.Server
	uiHandler, err := ui.New(deps, ui.Actions{
		Status:    func(r *http.Request) any { return srv.Status(r.Context()) },
		Workspace: func(r *http.Request, m minds.Mind) (api.WorkspaceView, error) { return srv.Workspace(r.Context(), m) },
		Memory:    func(r *http.Request, mind, id ids.ID) (api.MemoryDetail, error) { return srv.MemoryDetail(r, mind, id) },
		Belief:    func(r *http.Request, mind, id ids.ID) (api.BeliefDetail, error) { return srv.BeliefDetail(r, mind, id) },
		Post: func(r *http.Request, mind ids.ID, conv conversation.Conversation, text, who string) (conversation.Message, error) {
			return srv.PostMessage(r, mind, conv, text, who)
		},
		Study: func() *api.Server { return srv },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv = api.New(deps, uiHandler)
	h := &Harness{T: t, Deps: deps, Srv: httptest.NewServer(srv.Handler()), Admin: adminTok, Obs: obsTok}
	t.Cleanup(h.Srv.Close)
	return h
}

// Do performs an API request with a bearer token.
func (h *Harness) Do(token, method, path string, body any) (int, []byte) {
	h.T.Helper()
	var rd io.Reader = strings.NewReader("")
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, h.Srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.T.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// Mind creates an active mind through the API.
func (h *Harness) Mind(slug string) minds.Mind {
	h.T.Helper()
	code, _ := h.Do(h.Admin, "POST", "/api/v1/minds", map[string]any{"slug": slug, "display_name": slug})
	if code != 201 {
		h.T.Fatalf("create mind %s: %d", slug, code)
	}
	m, err := h.Deps.Minds.Resolve(context.Background(), slug)
	if err != nil {
		h.T.Fatal(err)
	}
	return m
}

// browser logs in through the UI and returns a client with the session cookie
// and the CSRF token.
// Browser logs in through the UI and returns a cookie client and CSRF token.
func (h *Harness) Browser(user, pass string) (*http.Client, string) {
	h.T.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.PostForm(h.Srv.URL+"/login", url.Values{"username": {user}, "password": {pass}})
	if err != nil || res.StatusCode != 303 {
		h.T.Fatalf("login: %v %d", err, res.StatusCode)
	}
	res.Body.Close()
	res, _ = c.Get(h.Srv.URL + "/account")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	i := strings.Index(string(body), `name="_csrf" value="`)
	if i < 0 {
		h.T.Fatal("no csrf token on page")
	}
	rest := string(body)[i+len(`name="_csrf" value="`):]
	return c, rest[:strings.Index(rest, `"`)]
}

// Settle gives the mind time to process and forces two cycles.
func (h *Harness) Settle(mind ids.ID) {
	time.Sleep(900 * time.Millisecond)
	h.Deps.Brain.Command(context.Background(), mind, "cycle", nil) //nolint:errcheck
	time.Sleep(400 * time.Millisecond)
	h.Deps.Brain.Command(context.Background(), mind, "cycle", nil) //nolint:errcheck
}

// EventsOf lists a mind's events of the given types.
func (h *Harness) EventsOf(mind ids.ID, types ...events.Type) []events.Event {
	evs, _ := h.Deps.Events.Query(context.Background(), evstore.Filter{MindID: mind, Types: types, Limit: 500})
	return evs
}
