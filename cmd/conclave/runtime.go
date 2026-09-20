package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/mcp"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/role"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tool"
)

type app struct {
	cfg    *config.Config
	llm    *llm.Client
	store  *store.Store
	bus    *event.Bus
	mcp    *mcp.Manager
	engine *orchestrator.Engine
}

func loadConfig() (*config.Config, error) {
	return config.Load(config.LoadOptions{
		GlobalDir:     flagGlobalDir,
		ProjectDir:    flagProjectDir,
		DisableGlobal: flagNoGlobal,
	})
}

func loadApp(ctx context.Context, withMCP bool) (*app, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	registry, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		return nil, err
	}
	llmClient := llm.New(registry, cfg.Settings.Timeout.Duration(), llm.RetryPolicy{
		MaxAttempts: cfg.Settings.Retry.MaxAttempts,
		BaseDelay:   cfg.Settings.Retry.BaseDelay.Duration(),
		MaxDelay:    cfg.Settings.Retry.MaxDelay.Duration(),
	})

	dataDir := flagDataDir
	if dataDir == "" {
		dataDir = flagProjectDir
	}
	if dataDir == "" {
		dataDir = config.DefaultProjectDir()
	}
	st, err := store.Open("", dataDir)
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}

	a := &app{
		cfg:   cfg,
		llm:   llmClient,
		store: st,
		bus:   event.NewBus(),
	}
	if withMCP && len(cfg.MCP) > 0 {
		a.mcp = mcp.NewManager(ctx, cfg.MCP)
	}
	a.engine = &orchestrator.Engine{
		Cfg:      cfg,
		LLM:      llmClient,
		Store:    st,
		Bus:      a.bus,
		Defaults: roleDefaults(cfg),
		MCP:      a.mcp,
		Asker:    newCLIAsker(),
		Out:      os.Stdout,
	}
	return a, nil
}

func (a *app) Close() {
	if a.mcp != nil {
		a.mcp.Close()
	}
	if a.store != nil {
		a.store.Close()
	}
}

func roleDefaults(cfg *config.Config) role.Defaults {
	d := cfg.Settings.Defaults
	allowed := []string{}
	if d.AllowedHosts != nil {
		allowed = d.AllowedHosts
	}
	return role.Defaults{
		FSRead:          boolOr(d.FSRead, true),
		FSWrite:         boolOr(d.FSWrite, false),
		Shell:           boolOr(d.Shell, false),
		Network:         boolOr(d.Network, true),
		AllowedHosts:    allowed,
		AllowedCommands: d.AllowedCommands,
		DeniedCommands:  d.DeniedCommands,
		CommandTimeout:  d.CommandTimeout.Duration(),
		MaxOutputBytes:  d.MaxOutputBytes,
		WebSearch: &tool.WebSearchConfig{
			Provider:   cfg.Settings.WebSearch.Provider,
			BaseURL:    cfg.Settings.WebSearch.BaseURL,
			APIKey:     cfg.Settings.WebSearch.APIKey,
			MaxResults: cfg.Settings.WebSearch.MaxResults,
			SearchType: cfg.Settings.WebSearch.SearchType,
		},
	}
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// buildRoleRuntime assembles a runtime for a single role (used by `ask`).
func (a *app) buildRoleRuntime(ctx context.Context, roleID, workspace string) (*role.Runtime, error) {
	def, ok := a.cfg.Roles[roleID]
	if !ok {
		return nil, fmt.Errorf("unknown role %q", roleID)
	}
	if workspace == "" {
		workspace = a.cfg.Settings.Workspace
	}
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	r := &role.Runtime{
		Def:           def,
		LLM:           a.llm,
		MaxIterations: a.cfg.Settings.MaxIterations,
		Bus:           a.bus,
		Model:         def.Model,
		Fallback:      def.Fallback,
	}
	env := role.BuildEnv(def, workspace, roleDefaults(a.cfg))
	env.Todos = orchestrator.NewState("ask", "", workspace)
	env.Asker = newCLIAsker()
	reg := newToolRegistry(env, a.mcp)
	filtered, err := reg.Filter(def.Tools)
	if err != nil {
		return nil, err
	}
	r.Tools = filtered
	return r, nil
}

// subscribeEvents prints progress to stderr.
func (a *app) subscribeEvents(ctx context.Context) func() {
	ch, cancel := a.bus.Subscribe(512)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				printEvent(ev)
			}
		}
	}()
	return cancel
}

func printEvent(ev event.Event) {
	ts := ev.Time.Format("15:04:05")
	switch ev.Type {
	case event.RoleDelta:
		fmt.Fprint(os.Stderr, ev.Message)
	case event.NodeStarted:
		fmt.Fprintf(os.Stderr, "\n[%s] ▶ %s (%s)\n", ts, ev.NodeID, ev.Message)
	case event.NodeFinished:
		fmt.Fprintf(os.Stderr, "\n[%s] ✓ %s: %s\n", ts, ev.NodeID, ev.Message)
	case event.ToolCall:
		fmt.Fprintf(os.Stderr, "\n[%s]   ⚙ %s %v\n", ts, ev.Message, ev.Data["arguments"])
	case event.ToolResult:
		fmt.Fprintf(os.Stderr, "[%s]   ↳ %s\n", ts, summarize(ev.Data))
	case event.ContextUsage:
		fmt.Fprintf(os.Stderr, "[%s]   ctx %s\n", ts, formatContext(ev.Data))
	case event.TodosUpdated:
		fmt.Fprintf(os.Stderr, "\n[%s] todos updated:\n%s\n", ts, renderTodos(ev.Data))
	case event.RunStarted:
		fmt.Fprintf(os.Stderr, "[%s] run started: %s\n", ts, ev.Message)
	case event.RunFinished:
		fmt.Fprintf(os.Stderr, "[%s] run finished: %s\n", ts, ev.Message)
	case event.Error:
		fmt.Fprintf(os.Stderr, "[%s] error: %s\n", ts, ev.Message)
	}
}

func formatContext(data map[string]any) string {
	tokens, _ := data["prompt_tokens"].(int)
	limit, _ := data["context_limit"].(int)
	msgs, _ := data["messages"].(int)
	if limit > 0 {
		pct := float64(tokens) / float64(limit) * 100
		return fmt.Sprintf("%s/%s (%.1f%%, %d msgs)", formatTokens(tokens), formatTokens(limit), pct, msgs)
	}
	return fmt.Sprintf("%s (limit unknown, %d msgs)", formatTokens(tokens), msgs)
}

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func renderTodos(data map[string]any) string {
	todos, ok := data["todos"].([]tool.Todo)
	if !ok {
		return ""
	}
	return tool.RenderTodos(todos)
}

func summarize(data map[string]any) string {
	if data == nil {
		return ""
	}
	s, _ := data["content"].(string)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func newToolRegistry(env *tool.Env, mgr *mcp.Manager) *tool.Registry {
	reg := tool.NewRegistry(env)
	if mgr != nil {
		for _, t := range mgr.Tools() {
			reg.Register(t)
		}
	}
	return reg
}
