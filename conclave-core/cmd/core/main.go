// Command core runs the conclave-core server: HTTP control plane + WebSocket
// endpoint for local agents.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/api"
	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/conf"
	"github.com/texhik/conclave/conclave-core/internal/config"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/obs"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/realtime"
	"github.com/texhik/conclave/conclave-core/internal/session"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
	"github.com/texhik/conclave/conclave-core/internal/store/postgres"
	"github.com/texhik/conclave/conclave-core/internal/worker"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "core:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)

	shutdownTracing := obs.InitTracing("conclave-core", logger)
	defer func() { _ = shutdownTracing(context.Background()) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer st.Close()

	secret := cfg.SecretKey
	if secret == "" {
		// In-memory dev mode: generate an ephemeral secret.
		secret = randomSecret()
		logger.Warn("CORE_SECRET_KEY not set; generated an ephemeral secret")
	}
	authMgr, err := auth.New(st, secret)
	if err != nil {
		return err
	}

	registry, err := playbook.LoadBuiltin()
	if err != nil {
		return err
	}
	if err := registry.LoadDir(cfg.PlaybookDir); err != nil {
		return fmt.Errorf("loading playbooks from %s: %w", cfg.PlaybookDir, err)
	}

	if err := conf.Seed(ctx, st, conf.SeedInput{
		Providers:       cfg.Providers,
		DefaultProvider: cfg.DefaultProvider,
		DefaultModel:    cfg.LLMModel,
		Roles:           registry.List(),
	}, logger); err != nil {
		return fmt.Errorf("seeding configuration: %w", err)
	}

	mgr := conf.New(st, cfg.LLMTimeout, logger)
	if err := mgr.Reload(ctx); err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	go mgr.Run(ctx, cfg.ConfigPoll)

	llm := llmgw.Client(&session.UsageRecorder{
		Client:     mgr,
		Store:      st,
		PriceIn:    cfg.PriceInputPer1M,
		PriceOut:   cfg.PriceOutputPer1M,
		ModelPrice: mgr.Price,
	})
	roleRunner := &orchestrator.LLMRoleRunner{Client: llm, DefaultModel: cfg.LLMModel}
	evLog := events.New(st)
	sup := &orchestrator.Supervisor{
		Brain:      &orchestrator.LLMBrain{Client: llm, DefaultModel: cfg.LLMModel, Models: mgr},
		Roles:      roleRunner,
		Critic:     &orchestrator.LLMCritic{Client: llm, DefaultModel: cfg.LLMModel, Models: mgr},
		Log:        evLog,
		Dispatcher: orchestrator.NewDispatcher(),
		MaxSteps:   16,
		MaxReopens: 2,
	}
	svc := session.NewService(st, evLog, mgr, sup)
	svc.MaxPlanVersions = cfg.MaxPlanVersions
	svc.DefaultBudget = cfg.BudgetUSD
	if svc.Scheduler != nil {
		svc.Scheduler.MaxParallel = cfg.MaxParallel
	}
	svc.Interviewer = session.NewInterviewer(st, evLog, llm, cfg.LLMModel)
	svc.Interviewer.Models = mgr
	svc.Planner = &session.Planner{
		Store: st, Log: evLog, Playbooks: mgr, Service: svc, Client: llm, Model: cfg.LLMModel, Models: mgr,
	}
	svc.Controller = session.NewController(svc, llm, cfg.LLMModel)
	svc.Controller.Models = mgr

	var clientWS http.Handler
	var agentWS http.Handler
	var cn *realtime.ClientNode
	if cfg.Fanout != "off" {
		redisAddr := ""
		if cfg.Fanout == "redis" {
			redisAddr = cfg.RedisAddr
		}
		rtCfg := realtime.Config{
			RedisAddr:   redisAddr,
			HistorySize: cfg.StreamHistorySize,
			HistoryTTL:  cfg.StreamHistoryTTL,
			CallTimeout: cfg.ToolTimeout,
			Logger:      logger,
		}
		cn, err = realtime.NewClientNode(rtCfg, authMgr, st)
		if err != nil {
			return err
		}
		defer func() { _ = cn.Shutdown(context.Background()) }()
		clientWS = cn.Handler()
		relay := &realtime.Relay{Log: evLog, Node: cn, Logger: logger}
		go relay.Run(ctx)

		an, err := realtime.NewAgentNode(rtCfg, authMgr, st)
		if err != nil {
			return err
		}
		defer func() { _ = an.Shutdown(context.Background()) }()
		agentWS = an.Handler()
		roleRunner.Tools = &realtime.AgentExecutor{Agents: an}
		logger.Info("realtime enabled", "fanout", cfg.Fanout)
	}

	// Token deltas are coalesced and persisted; they are published live when a
	// realtime node is available.
	var deltaPub session.DeltaPublisher
	if cn != nil {
		deltaPub = cn
	}
	coalescer := session.NewCoalescer(st, evLog, deltaPub, cfg.DeltaFlush)
	svc.DeltaSink = coalescer
	go coalescer.Run(ctx)

	srv := api.New(api.Deps{
		Store:          st,
		Events:         evLog,
		Auth:           authMgr,
		Sessions:       svc,
		Playbooks:      mgr,
		AdminKey:       cfg.SecretKey,
		Logger:         logger,
		RateLimitRPS:   cfg.RateLimitRPS,
		RateLimitBurst: cfg.RateLimitBurst,
		ClientWS:       clientWS,
		AgentWS:        agentWS,
	})

	host, _ := os.Hostname()
	for i := 0; i < cfg.Workers; i++ {
		w := &worker.Worker{
			Store: st, Service: svc, Log: evLog, Logger: logger,
			ID:    fmt.Sprintf("%s-%d-%d", host, os.Getpid(), i),
			Lease: cfg.JobLease, Poll: cfg.JobPoll, MaxAttempts: 3,
			HeartbeatInterval: 2 * time.Second,
		}
		go w.Run(ctx)
	}
	logger.Info("workers started", "count", cfg.Workers)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	logger.Info("conclave-core listening", "addr", cfg.ListenAddr, "in_memory", cfg.InMemory(), "version", version)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openStore(ctx context.Context, cfg *config.Config, logger *slog.Logger) (store.Store, error) {
	if cfg.InMemory() {
		logger.Warn("CORE_DB_DSN not set; using in-memory store (data is not persisted)")
		return memory.New(), nil
	}
	return postgres.Open(ctx, cfg.DBDSN)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "insecure-dev-secret"
	}
	return hex.EncodeToString(b)
}
