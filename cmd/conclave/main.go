package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/texhik/conclave/internal/app"
	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tui"
	"github.com/texhik/conclave/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "conclave:", err)
		os.Exit(1)
	}
}

func run() error {
	cwd, _ := os.Getwd()
	if len(os.Args) == 1 {
		return runTUI(cwd, "")
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("conclave", version)
		return nil
	case "help", "--help", "-h":
		fmt.Print(help)
		return nil
	case "config":
		return configCmd(cwd, args)
	case "models":
		return modelsCmd(cwd, args)
	case "run":
		return runHeadless(cwd, args)
	case "sessions":
		return sessionsCmd()
	case "serve":
		return serveCmd(args)
	case "tui":
		return runTUI(cwd, firstProfile(args))
	default:
		// Allow `conclave "prompt"` as a shortcut to headless run.
		if !strings.HasPrefix(cmd, "-") {
			return runHeadless(cwd, os.Args[1:])
		}
		return fmt.Errorf("unknown command %q\n\n%s", cmd, help)
	}
}

func runTUI(cwd, profile string) error {
	a, err := app.New(context.Background(), cwd, profile)
	if err != nil {
		return err
	}
	defer a.Close()
	p := tea.NewProgram(tui.New(a), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func configCmd(cwd string, args []string) error {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "init":
		path, err := config.Seed(cwd)
		if err != nil {
			return err
		}
		fmt.Println("created", path)
		fmt.Println("set DEEPSEEK_API_KEY and/or OPENROUTER_API_KEY, then run `conclave`")
		return nil
	case "path":
		fmt.Println(config.ProjectPath(cwd))
		return nil
	case "validate", "show":
		cfg, err := config.Load(cwd)
		if err != nil {
			return err
		}
		fmt.Println("config:", cfg.Path)
		fmt.Println("profile:", cfg.DefaultProfile)
		fmt.Println("providers:", len(cfg.Providers))
		for name, p := range cfg.Providers {
			fmt.Printf("  %s: %s (%d models, parallel %d)\n", name, p.BaseURL, len(p.Models), p.MaxParallel)
		}
		for name, p := range cfg.Profiles {
			fmt.Printf("profile %s: agent=%s workers=%d\n", name, p.Agent.Model, len(p.Workers))
		}
		return nil
	default:
		return fmt.Errorf("usage: conclave config [init|validate|show|path]")
	}
}

func modelsCmd(cwd string, args []string) error {
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}
	for pname, p := range cfg.Providers {
		for mname, m := range p.Models {
			fmt.Printf("%-10s %-40s ctx=%-8d out=%-6d\n", pname, mname, m.Context, m.MaxOutput)
		}
	}
	return nil
}

func serveCmd(args []string) error {
	addr := "127.0.0.1:8787"
	if cfg, err := config.LoadGlobalOnly(); err == nil && cfg.Server.Address != "" {
		addr = cfg.Server.Address
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr", "-a":
			if i+1 >= len(args) {
				return fmt.Errorf("--addr needs a value")
			}
			i++
			addr = args[i]
		case "--help", "-h":
			fmt.Println("usage: conclave serve [--addr 127.0.0.1:8787]")
			return nil
		}
	}
	srv := &http.Server{Addr: addr, Handler: web.NewServer().Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	fmt.Printf("conclave web UI: http://%s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func sessionsCmd() error {
	st, err := store.Open("")
	if err != nil {
		return err
	}
	defer st.Close()
	list, err := st.ListSessions(context.Background(), 30)
	if err != nil {
		return err
	}
	for _, s := range list {
		fmt.Printf("%s  %-8s %-28s %s\n", s.ID, s.Mode, truncate(s.Model, 28), s.Title)
	}
	return nil
}

func runHeadless(cwd string, args []string) error {
	profile := ""
	var promptParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value", args[i])
			}
			i++
			profile = args[i]
		case "-h", "--help":
			fmt.Println("usage: conclave run [-p profile] \"prompt\"")
			return nil
		default:
			promptParts = append(promptParts, args[i])
		}
	}
	prompt := strings.TrimSpace(strings.Join(promptParts, " "))
	if prompt == "" {
		return fmt.Errorf("prompt is required: conclave run \"...\"")
	}
	abs, _ := filepath.Abs(cwd)
	a, err := app.New(context.Background(), abs, profile)
	if err != nil {
		return err
	}
	defer a.Close()
	if a.State().Session.Mode == domain.ModeConclave {
		_ = a.SetMode(domain.ModeBuild)
	}
	events, unsub := a.Bus().Subscribe()
	defer unsub()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- a.Send(ctx, prompt) }()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch ev.Type {
			case "assistant.delta":
				fmt.Print(ev.Text)
			case "tool.started":
				if p, ok := ev.Payload.(map[string]any); ok {
					if call, ok := p["call"].(domain.ToolCall); ok {
						fmt.Fprintf(os.Stderr, "\n[tool] %s %s\n", call.Name, truncate(call.Arguments, 160))
					}
				}
			case "tool.failed":
				fmt.Fprintf(os.Stderr, "\n[tool failed] %s\n", ev.Text)
			case "usage.updated":
				if u, ok := ev.Payload.(domain.UsageRecord); ok {
					fmt.Fprintf(os.Stderr, "[usage] %s/%s %d→%d\n", u.Provider, u.Model, u.InputTokens, u.OutputTokens)
				}
			}
		case err := <-done:
			fmt.Println()
			if err != nil {
				return err
			}
			st := a.State()
			fmt.Fprintf(os.Stderr, "[done] context %d/%d\n", st.Session.ContextTokens, st.ContextLimit)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func firstProfile(args []string) string {
	for i := 0; i < len(args); i++ {
		if (args[i] == "-p" || args[i] == "--profile") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

const help = `conclave — terminal multi-agent coding assistant

Usage:
  conclave                      start the TUI
  conclave serve [--addr]       start the web UI (uses server.address config)
  conclave run "prompt"         run a single prompt headlessly
  conclave config init          create .conclave/config.yaml
  conclave config validate      validate configuration
  conclave config path          print the project config path
  conclave models               list configured models
  conclave sessions             list recent sessions
  conclave version

Environment:
  DEEPSEEK_API_KEY, OPENROUTER_API_KEY   provider keys
  CONCLAVE_DB                            override the SQLite database path
  CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT       first-token timeout (default 180s)
  CONCLAVE_LLM_IDLE_TIMEOUT              stream idle timeout (default 180s)
`
