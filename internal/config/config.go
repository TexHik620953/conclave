package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version          int                  `yaml:"version"`
	DefaultProfile   string               `yaml:"default_profile"`
	WorkspaceProfile string               `yaml:"workspace_profile,omitempty"`
	Providers        map[string]Provider  `yaml:"providers"`
	Profiles         map[string]Profile   `yaml:"profiles"`
	MCP              map[string]MCPServer `yaml:"mcp,omitempty"`
	Prompts          map[string]string    `yaml:"prompts,omitempty"`
	Permissions      Permissions          `yaml:"permissions,omitempty"`
	Search           SearchConfig         `yaml:"search,omitempty"`
	Server           ServerConfig         `yaml:"server,omitempty"`

	Path    string `yaml:"-"`
	Project string `yaml:"-"`
}

type Provider struct {
	BaseURL     string            `yaml:"base_url"`
	APIKey      string            `yaml:"api_key"`
	MaxParallel int               `yaml:"max_parallel,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Models      map[string]Model  `yaml:"models,omitempty"`
}

type Model struct {
	Context      int               `yaml:"context"`
	MaxOutput    int               `yaml:"max_output"`
	Capabilities Capabilities      `yaml:"capabilities,omitempty"`
	EffortMap    map[string]string `yaml:"effort_map,omitempty"`
}

type Capabilities struct {
	Tools     bool `yaml:"tools"`
	Reasoning bool `yaml:"reasoning"`
	Effort    bool `yaml:"effort"`
}

type Profile struct {
	Agent    AgentConfig    `yaml:"agent" json:"agent"`
	Master   AgentConfig    `yaml:"master" json:"master"`
	Workers  []WorkerConfig `yaml:"workers" json:"workers"`
	Limits   Limits         `yaml:"limits" json:"limits"`
	Timeouts TimeoutConfig  `yaml:"timeouts,omitempty" json:"timeouts,omitempty"`
}

type TimeoutConfig struct {
	FirstToken string `yaml:"first_token,omitempty" json:"first_token,omitempty"`
	Idle       string `yaml:"idle,omitempty" json:"idle,omitempty"`
}

func (t TimeoutConfig) Resolve() (time.Duration, time.Duration) {
	return durationSetting(t.FirstToken, "CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT"),
		durationSetting(t.Idle, "CONCLAVE_LLM_IDLE_TIMEOUT")
}

func durationSetting(raw, envName string) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && d > 0 {
		return d
	}
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(envName))); err == nil && d > 0 {
		return d
	}
	return 180 * time.Second
}

type AgentConfig struct {
	Model       string  `yaml:"model" json:"model"`
	Effort      string  `yaml:"effort" json:"effort"`
	Temperature float64 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	MaxSteps    int     `yaml:"max_steps,omitempty" json:"max_steps,omitempty"`
}

type WorkerConfig struct {
	ID         string   `yaml:"id" json:"id"`
	Model      string   `yaml:"model" json:"model"`
	Effort     string   `yaml:"effort" json:"effort"`
	Complexity []int    `yaml:"complexity,omitempty" json:"complexity,omitempty"`
	Instances  int      `yaml:"instances" json:"instances"`
	Tools      []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Prompt     string   `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	MaxSteps   int      `yaml:"max_steps,omitempty" json:"max_steps,omitempty"`
}

type Limits struct {
	MaxParallel   int `yaml:"max_parallel" json:"max_parallel"`
	MaxIterations int `yaml:"max_iterations" json:"max_iterations"`
}

type MCPServer struct {
	Type    string            `yaml:"type"`
	Command []string          `yaml:"command,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Env     map[string]string `yaml:"environment,omitempty"`
	Enabled *bool             `yaml:"enabled,omitempty"`
}

type Permissions struct {
	Shell               string `yaml:"shell,omitempty"` // allow | ask | deny
	Write               string `yaml:"write,omitempty"`
	OutsideCwd          string `yaml:"outside_cwd,omitempty"`
	Network             string `yaml:"network,omitempty"`
	AllowPrivateNetwork bool   `yaml:"allow_private_network,omitempty"`
	Yolo                bool   `yaml:"yolo,omitempty"`
}

type ServerConfig struct {
	Address string `yaml:"address,omitempty" json:"address,omitempty"`
}

type SearchConfig struct {
	Provider   string `yaml:"provider,omitempty" json:"provider"`
	APIKey     string `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL    string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	MaxResults int    `yaml:"max_results,omitempty" json:"max_results,omitempty"`
}

type ModelRef struct {
	Provider       string
	ID             string
	Config         Model
	ProviderConfig Provider
}

func Default() Config {
	return Config{
		Version:        1,
		DefaultProfile: "default",
		Providers:      map[string]Provider{},
		Profiles:       map[string]Profile{},
		Permissions: Permissions{
			Shell:      "allow",
			Write:      "allow",
			OutsideCwd: "deny",
			Network:    "allow",
		},
		Search: SearchConfig{Provider: "tavily", MaxResults: 5},
		Server: ServerConfig{Address: "127.0.0.1:8787"},
	}
}

func GlobalPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "conclave", "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "conclave", "config.yaml")
}

func ProjectPath(cwd string) string {
	return filepath.Join(cwd, ".conclave", "config.yaml")
}

// Load merges the global config with the project config. Project values win.
func Load(cwd string) (*Config, error) {
	_ = MigrateProjectToGlobal(cwd)
	cfg := Default()
	loaded := false
	for _, p := range []string{GlobalPath(), ProjectPath(cwd)} {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		next := Default()
		if err := yaml.Unmarshal(b, &next); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		merge(&cfg, next)
		cfg.Path = p
		loaded = true
	}
	if !loaded {
		return nil, fmt.Errorf("config not found; run `conclave config init`")
	}
	cfg.Project = cwd
	cfg.expandEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Path = path
	cfg.expandEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func merge(dst *Config, src Config) {
	if src.Version != 0 {
		dst.Version = src.Version
	}
	if src.DefaultProfile != "" {
		dst.DefaultProfile = src.DefaultProfile
	}
	if src.WorkspaceProfile != "" {
		dst.WorkspaceProfile = src.WorkspaceProfile
	}
	if dst.Providers == nil {
		dst.Providers = map[string]Provider{}
	}
	for k, v := range src.Providers {
		if old, ok := dst.Providers[k]; ok {
			if v.BaseURL == "" {
				v.BaseURL = old.BaseURL
			}
			if v.APIKey == "" {
				v.APIKey = old.APIKey
			}
			if v.MaxParallel == 0 {
				v.MaxParallel = old.MaxParallel
			}
			if v.Headers == nil {
				v.Headers = old.Headers
			}
			if v.Models == nil {
				v.Models = old.Models
			} else if old.Models != nil {
				models := map[string]Model{}
				for mk, mv := range old.Models {
					models[mk] = mv
				}
				for mk, mv := range v.Models {
					models[mk] = mv
				}
				v.Models = models
			}
		}
		dst.Providers[k] = v
	}
	if dst.Profiles == nil {
		dst.Profiles = map[string]Profile{}
	}
	for k, v := range src.Profiles {
		dst.Profiles[k] = v
	}
	if dst.MCP == nil {
		dst.MCP = map[string]MCPServer{}
	}
	for k, v := range src.MCP {
		dst.MCP[k] = v
	}
	if dst.Prompts == nil {
		dst.Prompts = map[string]string{}
	}
	for k, v := range src.Prompts {
		dst.Prompts[k] = v
	}
	if src.Permissions.Shell != "" {
		dst.Permissions.Shell = src.Permissions.Shell
	}
	if src.Permissions.Write != "" {
		dst.Permissions.Write = src.Permissions.Write
	}
	if src.Permissions.OutsideCwd != "" {
		dst.Permissions.OutsideCwd = src.Permissions.OutsideCwd
	}
	dst.Permissions.AllowPrivateNetwork = dst.Permissions.AllowPrivateNetwork || src.Permissions.AllowPrivateNetwork
	if src.Permissions.Network != "" {
		dst.Permissions.Network = src.Permissions.Network
	}
	dst.Permissions.Yolo = dst.Permissions.Yolo || src.Permissions.Yolo
	if src.Search.Provider != "" {
		dst.Search.Provider = src.Search.Provider
	}
	if src.Search.APIKey != "" {
		dst.Search.APIKey = src.Search.APIKey
	}
	if src.Search.BaseURL != "" {
		dst.Search.BaseURL = src.Search.BaseURL
	}
	if src.Search.MaxResults > 0 {
		dst.Search.MaxResults = src.Search.MaxResults
	}
	if src.Server.Address != "" {
		dst.Server.Address = src.Server.Address
	}
}

func (c *Config) expandEnv() {
	c.Server.Address = os.ExpandEnv(c.Server.Address)
	c.Search.APIKey = os.ExpandEnv(c.Search.APIKey)
	c.Search.BaseURL = os.ExpandEnv(c.Search.BaseURL)
	for name, srv := range c.MCP {
		srv.URL = os.ExpandEnv(srv.URL)
		for i := range srv.Command {
			srv.Command[i] = os.ExpandEnv(srv.Command[i])
		}
		for k, v := range srv.Headers {
			srv.Headers[k] = os.ExpandEnv(v)
		}
		for k, v := range srv.Env {
			srv.Env[k] = os.ExpandEnv(v)
		}
		c.MCP[name] = srv
	}
	for name, p := range c.Providers {
		p.APIKey = os.ExpandEnv(p.APIKey)
		p.BaseURL = os.ExpandEnv(p.BaseURL)
		for k, v := range p.Headers {
			p.Headers[k] = os.ExpandEnv(v)
		}
		c.Providers[name] = p
	}
}

func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return errors.New("config: no providers configured")
	}
	if c.Search.Provider == "" {
		c.Search.Provider = "tavily"
	}
	if c.Search.MaxResults <= 0 {
		c.Search.MaxResults = 5
	}
	if c.Server.Address == "" {
		c.Server.Address = "127.0.0.1:8787"
	}
	if c.DefaultProfile == "" {
		c.DefaultProfile = "default"
	}
	if _, ok := c.Profiles[c.DefaultProfile]; !ok {
		return fmt.Errorf("config: default profile %q not found", c.DefaultProfile)
	}
	for name, p := range c.Providers {
		if p.BaseURL == "" {
			return fmt.Errorf("config: provider %q has no base_url", name)
		}
		if strings.TrimRight(p.BaseURL, "/") == "" {
			return fmt.Errorf("config: provider %q has invalid base_url", name)
		}
		for mn, m := range p.Models {
			if m.Context <= 0 {
				return fmt.Errorf("config: model %s/%s has no context", name, mn)
			}
			if m.MaxOutput <= 0 {
				m.MaxOutput = 8192
				p.Models[mn] = m
			}
		}
		c.Providers[name] = p
	}
	for pn, p := range c.Profiles {
		for _, ref := range []string{p.Agent.Model, p.Master.Model} {
			if ref == "" {
				continue
			}
			if _, err := c.ResolveModel(ref); err != nil {
				return fmt.Errorf("config: profile %q: %w", pn, err)
			}
		}
		for wi := range p.Workers {
			w := &p.Workers[wi]
			if w.ID == "" {
				return fmt.Errorf("config: profile %q has worker without id", pn)
			}
			if _, err := c.ResolveModel(w.Model); err != nil {
				return fmt.Errorf("config: profile %q worker %q: %w", pn, w.ID, err)
			}
			if w.Instances <= 0 {
				w.Instances = 1
			}
		}
		c.Profiles[pn] = p
	}
	return nil
}

func (c *Config) Profile(name string) (Profile, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("profile %q not found", name)
	}
	if p.Limits.MaxParallel <= 0 {
		p.Limits.MaxParallel = 8
	}
	if p.Limits.MaxIterations <= 0 {
		p.Limits.MaxIterations = 6
	}
	if p.Agent.Effort == "" {
		p.Agent.Effort = "medium"
	}
	hasResearcher := false
	for _, w := range p.Workers {
		if w.ID == "researcher" {
			hasResearcher = true
		}
	}
	if !hasResearcher {
		p.Workers = append(p.Workers, WorkerConfig{
			ID: "researcher", Model: p.Agent.Model, Effort: "medium", Complexity: []int{1, 2, 3, 4}, Instances: 2,
			Tools: []string{"read_file", "list_dir", "glob", "grep", "git"},
		})
	}
	if p.Master.Effort == "" {
		p.Master.Effort = "high"
	}
	return p, nil
}

// ResolveModel accepts provider/model-id. Model IDs may contain slashes
// (OpenRouter), so only the first slash separates provider from model.
func (c *Config) ResolveModel(ref string) (ModelRef, error) {
	provider, id, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || id == "" {
		return ModelRef{}, fmt.Errorf("model %q must be provider/model", ref)
	}
	p, ok := c.Providers[provider]
	if !ok {
		return ModelRef{}, fmt.Errorf("provider %q not found for model %q", provider, ref)
	}
	m, ok := p.Models[id]
	if !ok {
		return ModelRef{}, fmt.Errorf("model %q not configured under provider %q", id, provider)
	}
	return ModelRef{Provider: provider, ID: id, Config: m, ProviderConfig: p}, nil
}

func (c *Config) Worker(profile Profile, id string) (WorkerConfig, error) {
	for _, w := range profile.Workers {
		if w.ID == id {
			return w, nil
		}
	}
	if len(profile.Workers) > 0 {
		return profile.Workers[0], nil
	}
	return WorkerConfig{}, fmt.Errorf("profile has no workers")
}

func EnsureSeed(cwd string) (string, error) {
	path := ProjectPath(cwd)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return Seed(cwd)
}

func Seed(cwd string) (string, error) {
	path := ProjectPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("%s already exists", path)
	}
	if err := os.WriteFile(path, []byte(seedYAML), 0o644); err != nil {
		return "", err
	}
	runsDir := filepath.Join(cwd, ".conclave", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(runsDir, ".gitignore"), []byte("*\n"), 0o644)
	}
	return path, nil
}

const seedYAML = `version: 1
default_profile: default

# API keys are read from the environment. Never commit real keys.
providers:
  deepseek:
    base_url: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
    max_parallel: 4
    models:
      deepseek-chat:
        context: 128000
        max_output: 8192
        capabilities: {tools: true, reasoning: false, effort: false}
      deepseek-reasoner:
        context: 128000
        max_output: 65536
        capabilities: {tools: true, reasoning: true, effort: false}

  openrouter:
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    max_parallel: 8
    models:
      deepseek/deepseek-chat:
        context: 128000
        max_output: 8192
        capabilities: {tools: true, reasoning: false, effort: false}
      anthropic/claude-sonnet-4:
        context: 200000
        max_output: 16384
        capabilities: {tools: true, reasoning: true, effort: true}

profiles:
  default:
    agent:
      model: openrouter/deepseek/deepseek-chat
      effort: medium
      max_steps: 120
    master:
      model: openrouter/anthropic/claude-sonnet-4
      effort: high
    workers:
      - id: coder
        model: openrouter/deepseek/deepseek-chat
        effort: medium
        complexity: [1, 2, 3]
        instances: 4
        tools: [read_file, write_file, edit_file, list_dir, glob, grep, shell, git]
      - id: senior
        model: openrouter/anthropic/claude-sonnet-4
        effort: high
        complexity: [3, 4, 5]
        instances: 2
        tools: [read_file, write_file, edit_file, list_dir, glob, grep, shell, git]
      - id: researcher
        model: openrouter/deepseek/deepseek-chat
        effort: medium
        complexity: [1, 2, 3, 4]
        instances: 3
        tools: [read_file, list_dir, glob, grep, git]
      - id: verifier
        model: openrouter/deepseek/deepseek-chat
        effort: low
        complexity: [1, 2, 3, 4, 5]
        instances: 2
        tools: [read_file, list_dir, grep, shell, git]
    limits:
      max_parallel: 8
      max_iterations: 6
    timeouts:
      first_token: 180s
      idle: 180s

permissions:
  shell: allow
  write: allow
  outside_cwd: deny
  network: allow
  allow_private_network: false

# Native web search. Supported: tavily, brave, searxng, exa.
# search:
#   provider: tavily
#   api_key: ${TAVILY_API_KEY}
#   max_results: 5

# MCP servers (also editable in the web UI).
# mcp:
#   playwright:
#     type: stdio
#     command: ["npx", "-y", "@playwright/mcp@latest"]

prompts: {}
`
