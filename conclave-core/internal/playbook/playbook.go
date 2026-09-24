// Package playbook defines playbook templates and a registry of them.
package playbook

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Control modes for a playbook.
const (
	ControlSupervisor    = "supervisor"
	ControlExplicitGraph = "explicit-graph"
)

// Role is a tier within a playbook, bound to a model and tools.
type Role struct {
	Tier  string   `yaml:"tier" json:"tier"`
	Model string   `yaml:"model" json:"model"`
	Tools []string `yaml:"tools" json:"tools,omitempty"`
}

// GraphNode is a step in an explicit-graph playbook.
type GraphNode struct {
	ID        string   `yaml:"id" json:"id"`
	Role      string   `yaml:"role" json:"role"`
	Prompt    string   `yaml:"prompt" json:"prompt,omitempty"`
	DependsOn []string `yaml:"depends_on" json:"depends_on,omitempty"`
}

// QuestionPolicy controls how a playbook asks questions.
type QuestionPolicy struct {
	// AutoAccept is inherit|always|never.
	AutoAccept string `yaml:"auto_accept" json:"auto_accept"`
	// MaxQuestions caps the number of questions from this playbook.
	MaxQuestions int `yaml:"max_questions" json:"max_questions"`
}

// Playbook is a reusable process template.
type Playbook struct {
	ID             string         `yaml:"id" json:"id"`
	Version        int            `yaml:"version" json:"version"`
	Title          string         `yaml:"title" json:"title"`
	Inputs         []string       `yaml:"inputs" json:"inputs,omitempty"`
	Outputs        []string       `yaml:"outputs" json:"outputs,omitempty"`
	Roles          []Role         `yaml:"roles" json:"roles,omitempty"`
	Control        string         `yaml:"control" json:"control"`
	Graph          []GraphNode    `yaml:"graph" json:"graph,omitempty"`
	Guidelines     string         `yaml:"guidelines" json:"guidelines,omitempty"`
	QuestionPolicy QuestionPolicy `yaml:"question_policy" json:"question_policy"`
}

// Validate checks a playbook for consistency.
func (p Playbook) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("playbook: id is required")
	}
	if p.Version <= 0 {
		return fmt.Errorf("playbook %s: version must be > 0", p.ID)
	}
	switch p.Control {
	case "":
		p.Control = ControlSupervisor
	case ControlSupervisor:
	case ControlExplicitGraph:
		if len(p.Graph) == 0 {
			return fmt.Errorf("playbook %s: explicit-graph requires graph nodes", p.ID)
		}
	default:
		return fmt.Errorf("playbook %s: unknown control %q", p.ID, p.Control)
	}
	for _, r := range p.Roles {
		if r.Tier == "" {
			return fmt.Errorf("playbook %s: role needs a tier", p.ID)
		}
		// An empty model falls back to the gateway default at runtime.
	}
	return nil
}

// Registry is an immutable set of playbooks keyed by id (latest version wins).
type Registry struct {
	byID map[string]Playbook
}

// Provider is the read surface consumers need from a playbook registry. The
// static Registry and the database-backed conf.Manager both implement it.
type Provider interface {
	Get(id string) (Playbook, bool)
	List() []Playbook
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: map[string]Playbook{}}
}

// Add registers a playbook, replacing an older version of the same id.
func (r *Registry) Add(p Playbook) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if existing, ok := r.byID[p.ID]; ok && existing.Version >= p.Version {
		return nil
	}
	r.byID[p.ID] = p
	return nil
}

// Get returns a playbook by id.
func (r *Registry) Get(id string) (Playbook, bool) {
	p, ok := r.byID[id]
	return p, ok
}

// List returns all playbooks sorted by id.
func (r *Registry) List() []Playbook {
	out := make([]Playbook, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LoadDir loads every *.yaml/*.yml playbook under a filesystem directory.
func (r *Registry) LoadDir(dir string) error {
	if dir == "" {
		return nil
	}
	return r.LoadFS(os.DirFS(dir), ".")
}

// LoadFS loads every *.yaml/*.yml playbook under dir in the given filesystem.
func (r *Registry) LoadFS(fsys fs.FS, dir string) error {
	return fs.WalkDir(fsys, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		var p Playbook
		if err := yaml.Unmarshal(data, &p); err != nil {
			return fmt.Errorf("playbook %s: %w", path, err)
		}
		if err := r.Add(p); err != nil {
			return fmt.Errorf("playbook %s: %w", path, err)
		}
		return nil
	})
}
