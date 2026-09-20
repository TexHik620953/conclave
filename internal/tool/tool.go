// Package tool provides the tools that roles can invoke.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/texhik/conclave/internal/provider"
)

// Env carries the sandbox and permission context for tool execution.
type Env struct {
	Workspace       string
	FSRead          bool
	FSWrite         bool
	Shell           bool
	Network         bool
	HTTP            *http.Client
	CommandTimeout  time.Duration
	MaxOutputBytes  int
	AllowedHosts    []string
	AllowedCommands []string
	DeniedCommands  []string

	// Todos and Asker are optional run-scoped capabilities.
	Todos TodoStore
	Asker Asker

	// WebSearch configures the web_search tool (nil disables it).
	WebSearch *WebSearchConfig
}

// WebSearchConfig configures the web_search tool.
type WebSearchConfig struct {
	Provider   string
	BaseURL    string
	APIKey     string
	MaxResults int
	SearchType string
}

// Result is the outcome of a tool call.
type Result struct {
	Content string
	IsError bool
}

// Tool is a callable capability exposed to a model.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// Registry is an ordered set of tools.
type Registry struct {
	env   *Env
	tools map[string]Tool
	order []string
}

// NewRegistry builds a registry containing all built-in tools permitted by env.
func NewRegistry(env *Env) *Registry {
	r := &Registry{env: env, tools: map[string]Tool{}}
	for _, t := range builtins(env) {
		r.Register(t)
	}
	return r
}

// Filter returns a registry restricted to the named tools. An empty list keeps
// all tools. Unknown names are reported.
func (r *Registry) Filter(names []string) (*Registry, error) {
	if len(names) == 0 {
		return r, nil
	}
	out := &Registry{env: r.env, tools: map[string]Tool{}}
	for _, n := range names {
		t, ok := r.tools[n]
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", n)
		}
		out.Register(t)
	}
	return out, nil
}

// Register adds a tool.
func (r *Registry) Register(t Tool) {
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
		sort.Strings(r.order)
	}
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names lists registered tool names.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Definitions returns provider tool declarations for all registered tools.
func (r *Registry) Definitions() []provider.Tool {
	defs := make([]provider.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		defs = append(defs, provider.Tool{
			Type: "function",
			Function: provider.FunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			},
		})
	}
	return defs
}

// Execute runs a tool by name.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, ok := r.tools[name]
	if !ok {
		return Result{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}, nil
	}
	return t.Execute(ctx, args)
}

func builtins(env *Env) []Tool {
	return []Tool{
		&readFile{env},
		&writeFile{env},
		&editFile{env},
		&listDir{env},
		&globTool{env},
		&grepTool{env},
		&shellTool{env},
		&gitTool{env},
		&webFetch{env},
		&applyPatch{env},
		&runTests{env},
		&webSearch{env},
		&todoWrite{env},
		&todoRead{env},
		&askUser{env},
	}
}

func schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}
