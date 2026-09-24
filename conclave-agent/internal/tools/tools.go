// Package tools implements the local tools an agent executes for the core:
// filesystem, shell, git and patch operations sandboxed to the workspace.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Result is the outcome of a tool call.
type Result struct {
	Content string
	IsError bool
}

// Tool is a callable capability.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (Result, error)
}

// Definition is the wire form registered with the core (matches llmgw.ToolDef).
type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Registry is the set of tools available to the agent.
type Registry struct {
	workspace string
	timeout   time.Duration
	tools     map[string]Tool
	order     []string
}

// NewRegistry builds a registry with the built-in tools.
func NewRegistry(workspace string, timeout time.Duration) *Registry {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	r := &Registry{workspace: workspace, timeout: timeout, tools: map[string]Tool{}}
	for _, t := range []Tool{
		&readFile{r}, &writeFile{r}, &editFile{r}, &listDir{r}, &globTool{r},
		&grepTool{r}, &shellTool{r}, &gitTool{r}, &applyPatch{r},
	} {
		r.Register(t)
	}
	return r
}

// Register adds a tool.
func (r *Registry) Register(t Tool) {
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names lists the registered tool names.
func (r *Registry) Names() []string { return append([]string{}, r.order...) }

// Definitions returns the tool definitions to register with the core.
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.order))
	for _, n := range r.order {
		t := r.tools[n]
		out = append(out, Definition{Name: t.Name(), Description: t.Description(), Parameters: t.Schema()})
	}
	return out
}

// Execute runs a named tool.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, ok := r.tools[name]
	if !ok {
		return Result{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}, nil
	}
	return t.Execute(ctx, args)
}

// resolvePath resolves a path inside the workspace, rejecting escapes and
// symlinks that point outside it.
func (r *Registry) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	ws := filepath.Clean(r.workspace)
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	p = filepath.Clean(p)
	if p != ws && !strings.HasPrefix(p, ws+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes workspace", p)
	}
	realWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		realWS = ws
	}
	existing := p
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	if resolved, err := filepath.EvalSymlinks(existing); err == nil {
		if rest, relErr := filepath.Rel(existing, p); relErr == nil {
			real := filepath.Join(resolved, rest)
			if real != realWS && !strings.HasPrefix(real, realWS+string(os.PathSeparator)) {
				return "", fmt.Errorf("path %q escapes workspace", p)
			}
			return real, nil
		}
	}
	return p, nil
}

func schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}
