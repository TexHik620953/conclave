// Command agent runs the conclave local agent: it connects to conclave-core
// over two WebSockets (events + tool execution) and serves a local Web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/texhik/conclave/conclave-agent/internal/api"
	"github.com/texhik/conclave/conclave-agent/internal/config"
	"github.com/texhik/conclave/conclave-agent/internal/conn"
	"github.com/texhik/conclave/conclave-agent/internal/corehttp"
	"github.com/texhik/conclave/conclave-agent/internal/hub"
	"github.com/texhik/conclave/conclave-agent/internal/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	flag.StringVar(&cfg.CoreURL, "core-url", cfg.CoreURL, "conclave-core base URL")
	flag.StringVar(&cfg.UserToken, "user-token", cfg.UserToken, "user token (cu_...) for the client WebSocket")
	flag.StringVar(&cfg.DeviceToken, "device-token", cfg.DeviceToken, "device token (cc_...) for the agent WebSocket")
	flag.StringVar(&cfg.Workspace, "workspace", cfg.Workspace, "directory tools operate in")
	flag.StringVar(&cfg.ListenAddr, "addr", cfg.ListenAddr, "local UI/API listen address")
	flag.Parse()

	if err := cfg.Finalize(); err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	core := corehttp.New(cfg.CoreURL, cfg.UserToken, cfg.DeviceToken)
	info, err := core.DeviceMe(ctx)
	if err != nil {
		return fmt.Errorf("authenticating device: %w", err)
	}

	reg := tools.NewRegistry(cfg.Workspace, cfg.CommandTimeout)
	h := hub.New()
	cn := conn.New(*cfg, info.DeviceID, reg, h, logger)
	if err := cn.Start(ctx); err != nil {
		return fmt.Errorf("connecting to core: %w", err)
	}
	defer cn.Close()

	srv := api.New(api.Deps{
		Config: *cfg, Conn: cn, Hub: h, Core: core, Tools: reg, Logger: logger,
	})

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	logger.Info("conclave agent", "ui", "http://"+cfg.ListenAddr, "device", info.DeviceID, "core", cfg.CoreURL)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
