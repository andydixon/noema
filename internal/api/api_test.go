package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/config"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
)

func testServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	pool := storage.TestPool(t, migrations.FS)
	cfg := config.Defaults()
	cfg.DatabaseURL = "postgres://test"
	log := slog.New(slog.DiscardHandler)
	deps := app.Deps{Config: cfg, Log: log, DB: pool, Auth: auth.NewService(pool, time.Hour, 2*time.Hour),
		Audit: audit.New(pool, log), Metrics: observability.NewMetrics(), Minds: minds.NewStore(pool), Memory: memory.NewStore(context.Background(), pool, nil), Events: evstore.NewStore(pool, evstore.NewBus(50), nil), Migrations: migrations.FS, Started: time.Now()}
	ctx := context.Background()
	group := scheduler.NewGroup(ctx, log)
	t.Cleanup(func() { group.Stop() })
	deps.Brain = brain.NewManager(brain.Config{DB: pool, Events: deps.Events, Minds: deps.Minds, Registry: brain.NewRegistry(), Pool: scheduler.NewPool(2), Log: log, Metrics: deps.Metrics}, group)
	u, err := deps.Auth.CreateUser(ctx, "admin", "administrator-pass", auth.RoleAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := deps.Auth.CreateAPIToken(ctx, u.ID, "t", nil)
	srv := httptest.NewServer(New(deps, nil).Handler())
	t.Cleanup(srv.Close)
	return srv, tok
}

func TestStatusAuthAndErrorEnvelope(t *testing.T) {
	srv, tok := testServer(t)
	res, _ := http.Get(srv.URL + "/api/v1/system/status")
	if res.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}
	var e ErrorBody
	json.NewDecoder(res.Body).Decode(&e) //nolint:errcheck
	if e.Error.Code != "unauthenticated" || e.Error.RequestID == "" {
		t.Fatalf("bad error envelope: %+v", e)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/system/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	var st SystemStatus
	json.NewDecoder(res.Body).Decode(&st) //nolint:errcheck
	if !st.Database.OK || len(st.Migrations) == 0 {
		t.Fatalf("status incomplete: %+v", st)
	}
	if res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP header")
	}

	// Unknown JSON fields and body limits are rejected as 400.
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/users", strings.NewReader(`{"username":"x","password":"long-enough-pass","role":"observer","bogus":1}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 400 {
		t.Fatalf("expected 400 for unknown field, got %d", res.StatusCode)
	}
	// Bearer requests are exempt from CSRF but still role-checked.
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/users", strings.NewReader(`{"username":"obs","password":"long-enough-pass","role":"observer"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", res.StatusCode)
	}
}
