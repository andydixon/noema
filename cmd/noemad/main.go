// Command noemad is the Noema daemon: API server, web UI, scheduler,
// integration manager and cognitive workers in one process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/capabilities/browser"
	"github.com/andydixon/noema/internal/capabilities/host"
	"github.com/andydixon/noema/internal/cognition"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/consolidation"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/dreaming"
	"github.com/andydixon/noema/internal/epistemics"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/experiments"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/identity"
	"github.com/andydixon/noema/internal/integrations"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/narrative"
	"github.com/andydixon/noema/internal/notifications"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/privacy"
	"github.com/andydixon/noema/internal/reflection"
	"github.com/andydixon/noema/internal/replay"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/ui"
	"github.com/andydixon/noema/internal/version"
	"github.com/andydixon/noema/internal/volition"
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
	authSvc.SetSecrets(sec)
	if !sec.CanEncrypt() {
		log.Warn("NOEMA_MASTER_KEY not set: API keys must be given as env: or file: references")
	}
	deps.LLMStore = router.NewStore(db)
	deps.LLM = router.NewRouter(deps.LLMStore, sec, log, metrics, cfg.MaxConcurrentLLM)
	mindRoles := func(ctx context.Context, id ids.ID) map[llm.Role]ids.ID {
		m, err := deps.Minds.Get(ctx, id)
		if err != nil || len(m.Config.LLMRoles) == 0 {
			return nil
		}
		out := map[llm.Role]ids.ID{}
		for role, model := range m.Config.LLMRoles {
			out[llm.Role(role)] = ids.ID(model)
		}
		return out
	}
	deps.LLM.MindRoles = mindRoles
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
	deps.Assertions = epistemics.NewStore(db)
	deps.Conversations = conversation.NewStore(db)
	deps.Goals = goals.NewStore(db)
	deps.Motivation = motivation.NewStore(db)
	deps.Predictions = predictions.NewStore(db)
	deps.Resolver = &predictions.Resolver{Store: deps.Predictions, Events: deps.Events, Beliefs: deps.Beliefs, Log: log, Metrics: metrics}
	deps.Graph = graph.NewStore(db)
	capReg := capabilities.NewRegistry()
	for _, c := range capabilities.Builtins() {
		capReg.Add(c)
	}
	deps.Integrations = &integrations.Manager{Store: integrations.NewStore(db), Events: deps.Events, Graph: deps.Graph, Secrets: sec, Log: log, Pool: scheduler.NewPool(2),
		Pollers: map[integrations.Kind]integrations.Poller{integrations.ResearchFolder: integrations.ResearchPoller{}, integrations.Git: integrations.GitPoller{DataDir: "./data/git"}, integrations.Prometheus: integrations.PrometheusPoller{Secrets: sec}}}
	for _, c := range integrations.Capabilities(deps.Integrations, integrations.GitPoller{DataDir: "./data/git"}, integrations.PrometheusPoller{Secrets: sec}) {
		capReg.Add(c)
	}
	for _, c := range host.Capabilities(host.Options{Runner: host.ExecRunner{}}) {
		capReg.Add(c)
	}
	for _, c := range browser.Capabilities(browser.Options{}) {
		capReg.Add(c)
	}
	capReg.Freeze()
	deps.Capabilities = &capabilities.Service{Registry: capReg, DB: db, Events: deps.Events, Log: log, Metrics: metrics, ApprovalTTL: 24 * time.Hour, SessionTTL: 8 * time.Hour}
	selfSync := cognition.SelfModelSync{Minds: deps.Minds, Caps: deps.Capabilities, Events: deps.Events, Log: log}
	deps.Capabilities.OnSelfModel = selfSync.Sync
	deps.Narrative = narrative.NewStore(db)
	deps.Notifications = notifications.NewStore(db)
	deps.Consolidation = &consolidation.Service{DB: db, Memory: deps.Memory, Beliefs: deps.Beliefs, Events: deps.Events, LLM: deps.LLM, Prompts: deps.Prompts, Minds: deps.Minds, Log: log}
	deps.Dreaming = &dreaming.Service{DB: db, Memory: deps.Memory, Beliefs: deps.Beliefs, Events: deps.Events, LLM: deps.LLM, Prompts: deps.Prompts, Log: log, Volition: volition.NewStore(db)}
	deps.Reflection = &reflection.Service{DB: db, Events: deps.Events, Beliefs: deps.Beliefs, Goals: deps.Goals, Memory: deps.Memory, Predictions: deps.Predictions, LLM: deps.LLM, Prompts: deps.Prompts, Log: log}
	deps.Volition = volition.NewStore(db)
	deps.Plans = planning.NewStore(db)
	deps.Executor = &planning.Executor{Plans: deps.Plans, Volition: deps.Volition, Caps: deps.Capabilities, Predictions: deps.Predictions, Resolver: deps.Resolver, Events: deps.Events, Minds: deps.Minds, Memory: deps.Memory, Log: log,
		Planner: &planning.Planner{Registry: capReg, Effect: planning.PolicyEffect(deps.Capabilities), Model: planning.ModelPlanner(deps.LLM, deps.Prompts)}}
	deps.Research = research.NewStore(db)
	deps.Agency = &agency.Service{DB: db, Caps: deps.Capabilities, Plans: deps.Plans, Volition: deps.Volition, Goals: deps.Goals, Beliefs: deps.Beliefs, Predictions: deps.Predictions, Events: deps.Events}
	registry := cognition.NewRegistry(cognition.Stores{Agency: deps.Agency, Research: deps.Research, Memory: deps.Memory, Beliefs: deps.Beliefs, Conversations: deps.Conversations, Graph: deps.Graph, Assertions: deps.Assertions,
		Motivation: deps.Motivation, Narrative: deps.Narrative, Notifications: deps.Notifications})
	brainCfg := brain.Config{DB: db, Events: deps.Events, Minds: deps.Minds, Registry: registry, Pool: scheduler.NewPool(cfg.MaxWorkers),
		Log: log, Metrics: metrics, Services: brain.Services{Events: deps.Events, Memory: deps.Memory, Beliefs: deps.Beliefs, LLM: deps.LLM, Prompts: deps.Prompts,
			Goals: deps.Goals, Motivation: deps.Motivation, Predictions: deps.Predictions, Capabilities: deps.Capabilities, Volition: deps.Volition, Plans: deps.Plans, Executor: deps.Executor},
		AfterCommit: deps.Executor.WakeOn()}
	deps.Brain = brain.NewManager(brainCfg, group)
	// Sandbox runtimes (replay, experiments) share the registry but use a
	// router that serves recorded model responses when it can.
	replayLLM := router.NewRouter(deps.LLMStore, sec, log, metrics, cfg.MaxConcurrentLLM)
	replayLLM.Replay = true
	replayLLM.MindRoles = mindRoles
	replayLLM.DailyTokenBudget, replayLLM.MindBudget = deps.LLM.DailyTokenBudget, deps.LLM.MindBudget
	sandboxCfg := brainCfg
	sandboxCfg.Pool = scheduler.NewPool(2)
	sandboxCfg.Services.LLM = replayLLM
	sandboxCfg.AfterCommit = nil // sandbox minds plan but never act
	ident := &identity.Capturer{DB: db, Config: &cfg, Minds: deps.Minds, LLM: deps.LLMStore, Prompts: deps.Prompts, Capabilities: deps.Capabilities}
	deps.Snapshots = &snapshots.Store{DB: db, Events: deps.Events, Minds: deps.Minds, Identity: ident}
	deps.Privacy = &privacy.Service{DB: db, Events: deps.Events, Audit: auditLog, Snapshots: deps.Snapshots, Log: log}
	deps.Replay = &replay.Service{DB: db, Events: deps.Events, Minds: deps.Minds, Snapshots: deps.Snapshots, Brain: sandboxCfg, Identity: ident, Log: log}
	deps.Experiments = &experiments.Service{DB: db, Minds: deps.Minds, Snapshots: deps.Snapshots, Brain: sandboxCfg, Identity: ident, Log: log, Routing: func(ctx context.Context) []map[string]any {
		roles, _ := deps.LLMStore.ListRoles(ctx)
		b, _ := json.Marshal(roles)
		var out []map[string]any
		json.Unmarshal(b, &out) //nolint:errcheck
		return out
	}}
	selfSync.Sync(ctx, ids.Nil)
	group.Every("integrations.tick", time.Minute, deps.Integrations.Tick)
	group.Every("approvals.expire", 5*time.Minute, func(ctx context.Context) error {
		if _, err := deps.Capabilities.ExpirePending(ctx); err != nil {
			return err
		}
		_, err := deps.Capabilities.ExpireCapabilityRequests(ctx)
		return err
	})
	group.Every("grants.sweep", time.Minute, func(ctx context.Context) error { _, err := deps.Capabilities.SweepGrants(ctx); return err })
	group.Every("predictions.resolve", time.Minute, deps.Resolver.Run)
	group.Go("planning.executor", func(ctx context.Context) error { return deps.Executor.Run(ctx, 15*time.Second) })
	if planEvents, err := deps.Events.Bus().Subscribe(group.Context(), ids.Nil, 512); err == nil {
		group.Go("planning.react", func(ctx context.Context) error { return deps.Executor.Listen(ctx, planEvents) })
	} else {
		log.Warn("plan reactions disabled: no event subscription", "err", err)
	}
	group.Every("cognition.background", 5*time.Minute, func(ctx context.Context) error { return backgroundCognition(ctx, deps) })
	group.Every("memory.decay", 6*time.Hour, func(ctx context.Context) error { return decayMemories(ctx, deps) })
	group.Every("memory.embed", time.Minute, func(ctx context.Context) error { _, err := deps.Memory.EmbedMissing(ctx, 50); return err })
	if err := deps.Brain.Start(ctx); err != nil {
		return fmt.Errorf("start minds: %w", err)
	}

	var srv *api.Server
	uiHandler, err := ui.New(deps, ui.Actions{
		Status:    func(r *http.Request) any { return srv.Status(r.Context()) },
		Context:   func(r *http.Request, id ids.ID) (api.EventContext, error) { return srv.Context(r, id) },
		Workspace: func(r *http.Request, m minds.Mind) (api.WorkspaceView, error) { return srv.Workspace(r.Context(), m) },
		Memory:    func(r *http.Request, mind, id ids.ID) (api.MemoryDetail, error) { return srv.MemoryDetail(r, mind, id) },
		Belief:    func(r *http.Request, mind, id ids.ID) (api.BeliefDetail, error) { return srv.BeliefDetail(r, mind, id) },
		Emitter:   func() beliefs.Emitter { return srv.Emitter() },
		Post: func(r *http.Request, mind ids.ID, conv conversation.Conversation, text, who string) (conversation.Message, error) {
			return srv.PostMessage(r, mind, conv, text, who)
		},
		Goal: func(r *http.Request, n goals.New) (goals.Goal, error) { return srv.CreateGoal(r, n) },
		GoalUpdate: func(r *http.Request, mind, id ids.ID, u goals.Update) (goals.Goal, error) {
			return srv.UpdateGoal(r, mind, id, u)
		},
		Predict: func(r *http.Request, n predictions.New) (predictions.Prediction, error) {
			return srv.CreatePrediction(r, n)
		},
		Resolve: func(r *http.Request, mind, id ids.ID, outcome predictions.Status, reason string) (predictions.Result, error) {
			return srv.ResolvePrediction(r, mind, id, outcome, reason)
		},
		ResetState:  func(r *http.Request, mind ids.ID) (motivation.State, error) { return srv.ResetState(r, mind) },
		Consolidate: func(r *http.Request, mind ids.ID) (consolidation.Job, error) { return srv.RunConsolidation(r, mind) },
		Study:       func() *api.Server { return srv },
		Reflect:     func(r *http.Request, mind ids.ID) (reflection.Reflection, error) { return srv.RunReflection(r, mind) },
		Entity:      func(r *http.Request, mind, id ids.ID) (api.EntityDetail, error) { return srv.EntityDetail(r, mind, id) },
		Merge: func(r *http.Request, mind ids.ID, e graph.Entity, other string) error {
			return srv.MergeEntities(r, mind, e, other)
		},
		Distinct: func(r *http.Request, mind ids.ID, e graph.Entity, other string) error {
			return srv.DistinctEntities(r, mind, e, other)
		},
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

// backgroundCognition runs consolidation and reflection for each active mind
// whose interval has elapsed, switching the runtime mode while it works.
func backgroundCognition(ctx context.Context, d app.Deps) error {
	list, err := d.Minds.List(ctx, false)
	if err != nil {
		return err
	}
	for _, m := range list {
		if m.Status != minds.StatusActive {
			continue
		}
		if m.Config.ConsolidationEnabled && !consolidation.IsPaused() {
			if jobs, _ := d.Consolidation.Jobs(ctx, m.ID, 1); len(jobs) == 0 || time.Since(jobs[0].CreatedAt) >= m.Config.ConsolidationInterval.D() {
				d.Brain.Command(ctx, m.ID, "setmode", map[string]bool{"consolidating": true}) //nolint:errcheck
				if _, err := d.Consolidation.Run(ctx, m, "schedule", "noemad"); err != nil {
					d.Log.Warn("consolidation failed", "mind", m.Slug, "err", err)
				}
				d.Brain.Command(ctx, m.ID, "setmode", map[string]bool{"awake": true}) //nolint:errcheck
			}
		}
		if m.Config.DreamingEnabled && !dreaming.IsPaused() && dreaming.InWindow(m.Config.DreamHours, time.Now()) {
			if ds, _ := d.Dreaming.Dreams(ctx, m.ID, 1); len(ds) == 0 || time.Since(ds[0].CreatedAt) >= m.Config.DreamInterval.D() {
				d.Brain.Command(ctx, m.ID, "setmode", map[string]bool{"dreaming": true}) //nolint:errcheck
				if _, err := d.Dreaming.Run(ctx, m, "schedule", "noemad"); err != nil {
					d.Log.Warn("dream failed", "mind", m.Slug, "err", err)
				}
				d.Brain.Command(ctx, m.ID, "setmode", map[string]bool{"awake": true}) //nolint:errcheck
			}
		}
		if m.Config.ReflectionEnabled {
			if last, err := d.Reflection.Latest(ctx, m.ID); err != nil || time.Since(last.CreatedAt) >= m.Config.ReflectionInterval.D() {
				d.Brain.Command(ctx, m.ID, "setmode", map[string]bool{"reflecting": true}) //nolint:errcheck
				if _, err := d.Reflection.Run(ctx, m, "schedule", "noemad"); err != nil {
					d.Log.Warn("reflection failed", "mind", m.Slug, "err", err)
				}
				d.Brain.Command(ctx, m.ID, "setmode", map[string]bool{"awake": true}) //nolint:errcheck
			}
		}
	}
	return nil
}
