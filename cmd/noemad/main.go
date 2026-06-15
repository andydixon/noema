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
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/ui"
	"github.com/andydixon/noema/internal/version"
	"github.com/andydixon/noema/migrations"
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
	deps := app.Deps{Config: cfg, Log: log, DB: db, Auth: authSvc, Audit: auditLog, Metrics: metrics, Minds: minds.NewStore(db), Migrations: migrations.FS, Started: time.Now()}

	if err := bootstrapAdmin(ctx, deps); err != nil {
		return err
	}

	var srv *api.Server
	uiHandler, err := ui.New(deps, func(r *http.Request) any { return srv.Status(r.Context()) })
	if err != nil {
		return err
	}
	srv = api.New(deps, uiHandler)

	group := scheduler.NewGroup(ctx, log)
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
