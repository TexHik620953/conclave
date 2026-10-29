package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/llm"
)

type Env struct {
	Cwd         string
	Mode        domain.Mode
	Permissions config.Permissions
	RunID       string
	TaskID      string
	AgentID     string
	SessionID   string
	Extra       map[string]any
}

func (e Env) ExtraString(key string) string {
	if e.Extra == nil {
		return ""
	}
	if v, ok := e.Extra[key].(string); ok {
		return v
	}
	return ""
}

type Tool interface {
	Name() string
	Spec() llm.Tool
	Run(ctx context.Context, env Env, args json.RawMessage) (string, error)
}

type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Add(t Tool) {
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Specs() []llm.Tool {
	out := make([]llm.Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			out = append(out, t.Spec())
		}
	}
	return out
}

func (r *Registry) Names() []string {
	return append([]string(nil), r.order...)
}

func (r *Registry) Clone() *Registry {
	n := NewRegistry()
	for _, name := range r.order {
		n.Add(r.tools[name])
	}
	return n
}

func (r *Registry) Only(names ...string) *Registry {
	if len(names) == 0 {
		return r.Clone()
	}
	allowed := map[string]bool{}
	for _, n := range names {
		allowed[n] = true
	}
	n := NewRegistry()
	for _, name := range r.order {
		if allowed[name] {
			n.Add(r.tools[name])
		}
	}
	return n
}

func (r *Registry) Without(names ...string) *Registry {
	denied := map[string]bool{}
	for _, n := range names {
		denied[n] = true
	}
	n := NewRegistry()
	for _, name := range r.order {
		if !denied[name] {
			n.Add(r.tools[name])
		}
	}
	return n
}

func Builtins() *Registry {
	r := NewRegistry()
	r.Add(readFile{})
	r.Add(writeFile{})
	r.Add(editFile{})
	r.Add(listDir{})
	r.Add(globTool{})
	r.Add(grepTool{})
	r.Add(shellTool{})
	r.Add(gitTool{})
	r.Add(webFetch{})
	return r
}

func ReadOnly() *Registry {
	return Builtins().Only("read_file", "list_dir", "glob", "grep", "git", "web_fetch")
}

func safePath(cwd, p string, perms config.Permissions) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	if perms.OutsideCwd == "allow" {
		return p, nil
	}
	rel, err := filepath.Rel(cwd, p)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q is outside workspace", p)
	}
	return p, nil
}

func bound(env Env) error {
	if env.Mode == domain.ModePlan {
		return errors.New("write operations are disabled in plan mode")
	}
	if env.Permissions.Write == "deny" {
		return errors.New("write operations are disabled by permissions")
	}
	return nil
}

func input(args json.RawMessage, v any) error {
	if len(args) == 0 {
		return nil
	}
	return json.Unmarshal(args, v)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n...[truncated %d bytes]", len(s)-max)
}

func readText(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	_ = max
	return string(b), nil
}
