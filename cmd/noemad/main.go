// Command noemad is the Noema daemon: API server, web UI, scheduler,
// integration manager and cognitive workers in one process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/cognition"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/conversation"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/ui"
	"github.com/andydixon/noema/internal/version"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "noemad:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath = flag.String("config", "", "path to JSON config file (optional)")
		migrate = flag.Bool("migrate", true, "apply pending migrations at start")
		show    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *show {
		fmt.Println(version.Version)
		return nil
	}
	cfg, err := config.Load(*cfgPath, os.LookupEnv)
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	log.Info("starting noemad", "version", version.Version, "env", cfg.Env, "addr", cfg.HTTPAddr)
	log.Debug("configuration", "config", fmt.Sprintf("%+v", cfg.Redacted()))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if *migrate {
		applied, err := storage.MigrateUp(ctx, db, migrations.FS)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if len(applied) > 0 {
			log.Info("applied migrations", "migrations", applied)
		}
	}

	metrics := observability.NewMetrics()
	authSvc := auth.NewService(db, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	auditLog := audit.New(db, log)
	deps := app.Deps{Config: cfg, Log: log, DB: db, Auth: authSvc, Audit: auditLog, Metrics: metrics, Minds: minds.NewStore(db), Events: evstore.NewStore(db, evstore.NewBus(200), metrics), Migrations: migrations.FS, Started: time.Now()}

	if err := bootstrapAdmin(ctx, deps); err != nil {
		return err
	}

	group := scheduler.NewGroup(ctx, log)
	sec, err := secrets.New(cfg.MasterKey)
	if err != nil {
		return err
	}
	deps.Secrets = sec
	if !sec.CanEncrypt() {
		log.Warn("NOEMA_MASTER_KEY not set: API keys must be given as env: or file: references")
	}
	deps.LLMStore = router.NewStore(db)
	deps.LLM = router.NewRouter(deps.LLMStore, sec, log, metrics, cfg.MaxConcurrentLLM)
	deps.LLM.DailyTokenBudget = cfg.DailyTokenBudget
	deps.LLM.MindBudget = func(id ids.ID) int64 {
		m, err := deps.Minds.Get(ctx, id)
		if err != nil {
			return 0
		}
		return m.Config.DailyTokenBudget
	}
	deps.Prompts = prompts.NewStore(db)
	if n, err := deps.Prompts.SeedDefaults(ctx); err != nil {
		return fmt.Errorf("seed prompts: %w", err)
	} else if n > 0 {
		log.Info("installed default prompts", "count", n)
	}
	deps.Memory = memory.NewStore(ctx, db, deps.LLM.Embedder(ctx))
	deps.Beliefs = beliefs.NewStore(db)
	deps.Conversations = conversation.NewStore(db)
	registry := brain.NewRegistry().AddProcess(cognition.Attend{}).AddProcess(cognition.Retrieve{}).AddProcess(cognition.Associate{}).AddProcess(cognition.Perceive{})
	cognition.Handlers{Memory: deps.Memory, Beliefs: deps.Beliefs, Conversations: deps.Conversations}.Register(registry)
	deps.Brain = brain.NewManager(brain.Config{DB: db, Events: deps.Events, Minds: deps.Minds, Registry: registry, Pool: scheduler.NewPool(cfg.MaxWorkers),
		Log: log, Metrics: metrics, Services: brain.Services{Events: deps.Events, Memory: deps.Memory, Beliefs: deps.Beliefs, LLM: deps.LLM, Prompts: deps.Prompts}}, group)
	group.Every("memory.decay", 6*time.Hour, func(ctx context.Context) error { return decayMemories(ctx, deps) })
	group.Every("memory.embed", time.Minute, func(ctx context.Context) error { _, err := deps.Memory.EmbedMissing(ctx, 50); return err })
	if err := deps.Brain.Start(ctx); err != nil {
		return fmt.Errorf("start minds: %w", err)
	}

	var srv *api.Server
	uiHandler, err := ui.New(deps, func(r *http.Request) any { return srv.Status(r.Context()) },
		func(r *http.Request, id ids.ID) (api.EventContext, error) { return srv.Context(r, id) },
		func(r *http.Request, m minds.Mind) (api.WorkspaceView, error) { return srv.Workspace(r.Context(), m) },
		func(r *http.Request, mind, id ids.ID) (api.MemoryDetail, error) { return srv.MemoryDetail(r, mind, id) },
		func(r *http.Request, mind, id ids.ID) (api.BeliefDetail, error) { return srv.BeliefDetail(r, mind, id) },
		func() beliefs.Emitter { return srv.Emitter() },
		func(r *http.Request, mind ids.ID, conv conversation.Conversation, text, who string) (conversation.Message, error) {
			return srv.PostMessage(r, mind, conv, text, who)
		})
	if err != nil {
		return err
	}
	srv = api.New(deps, uiHandler)

	httpSrv := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	group.Go("http", func(ctx context.Context) error {
		errc := make(chan error, 1)
		go func() { errc <- httpSrv.ListenAndServe() }() // owned: joined via errc below
		select {
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return httpSrv.Shutdown(shutdownCtx)
		}
	})
	group.Every("auth.sweep", 5*time.Minute, func(context.Context) error { authSvc.SweepLimiter(); return nil })
	group.Every("auth.purge", time.Hour, authSvc.PurgeExpiredSessions)

	log.Info("noemad ready", "url", "http://"+cfg.HTTPAddr)
	<-ctx.Done()
	log.Info("shutting down")
	return group.Stop()
}

// bootstrapAdmin creates the first administrator from the environment when the
// users table is empty, so headless deployments can start without the setup page.
func bootstrapAdmin(ctx context.Context, d app.Deps) error {
	n, err := d.Auth.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 || d.Config.BootstrapAdminPassword == "" {
		return nil
	}
	u, err := d.Auth.CreateUser(ctx, d.Config.BootstrapAdminUser, d.Config.BootstrapAdminPassword, auth.RoleAdministrator)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	d.Audit.Log(ctx, "user.bootstrap", "user", u.ID.String(), map[string]any{"username": u.Username, "via": "environment"})
	d.Log.Info("created bootstrap administrator", "username", u.Username)
	return nil
}

// decayMemories lowers activation of unused memories for every mind and leaves
// one summary event per mind so the decay is visible without spamming.
func decayMemories(ctx context.Context, d app.Deps) error {
	list, err := d.Minds.List(ctx, false)
	if err != nil {
		return err
	}
	for _, m := range list {
		n, err := d.Memory.Decay(ctx, m.ID, 14*24*time.Hour)
		if err != nil {
			return err
		}
		if n > 0 {
			d.Events.Append(ctx, events.New{MindID: m.ID, Type: events.MemoryDecayed, Source: "memory.decay", Summary: fmt.Sprintf("activation decayed on %d memories", n), //nolint:errcheck
				Salience: 0.05, Confidence: 1, Metadata: map[string]any{"count": n}})
		}
	}
	return nil
}
