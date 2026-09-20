package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (c *Config) applyDefaults() {
	for name, p := range c.Providers {
		if p.APIKey == "" && p.APIKeyEnv != "" {
			p.APIKey = os.Getenv(p.APIKeyEnv)
		}
		c.Providers[name] = p
	}
	if c.Settings.MaxParallel <= 0 {
		c.Settings.MaxParallel = 4
	}
	if c.Settings.MaxIterations <= 0 {
		c.Settings.MaxIterations = 12
	}
	if c.Settings.MaxTokens <= 0 {
		c.Settings.MaxTokens = 8192
	}
	if c.Settings.Timeout <= 0 {
		c.Settings.Timeout = Duration(5 * time.Minute)
	}
	if c.Settings.Defaults.FSRead == nil {
		c.Settings.Defaults.FSRead = boolPtr(true)
	}
	if c.Settings.Defaults.FSWrite == nil {
		c.Settings.Defaults.FSWrite = boolPtr(false)
	}
	if c.Settings.Defaults.Shell == nil {
		c.Settings.Defaults.Shell = boolPtr(false)
	}
	if c.Settings.Defaults.Network == nil {
		c.Settings.Defaults.Network = boolPtr(true)
	}
	if c.Settings.Defaults.CommandTimeout <= 0 {
		c.Settings.Defaults.CommandTimeout = Duration(2 * time.Minute)
	}
	if c.Settings.Defaults.MaxOutputBytes <= 0 {
		c.Settings.Defaults.MaxOutputBytes = 64 * 1024
	}
	switch c.Settings.OnNoUser {
	case "next", "else", "stop":
	default:
		c.Settings.OnNoUser = "next"
	}
	if c.Settings.Retry.MaxAttempts <= 0 {
		c.Settings.Retry.MaxAttempts = 3
	}
	if c.Settings.Retry.BaseDelay <= 0 {
		c.Settings.Retry.BaseDelay = Duration(time.Second)
	}
	if c.Settings.Retry.MaxDelay <= 0 {
		c.Settings.Retry.MaxDelay = Duration(30 * time.Second)
	}
	switch c.Settings.OnBudget {
	case "warn", "abort":
	default:
		c.Settings.OnBudget = "warn"
	}
	if c.Settings.SummarizeThreshold == nil {
		c.Settings.SummarizeThreshold = floatPtr(0.8)
	}
	if c.Settings.WebSearch.Provider == "" {
		c.Settings.WebSearch.Provider = "exa"
	}
	if c.Settings.WebSearch.MaxResults <= 0 {
		c.Settings.WebSearch.MaxResults = 5
	}
	if c.Settings.WebSearch.SearchType == "" {
		c.Settings.WebSearch.SearchType = "auto"
	}
	if c.Settings.WebSearch.BaseURL == "" {
		switch c.Settings.WebSearch.Provider {
		case "searxng":
			c.Settings.WebSearch.BaseURL = "http://localhost:8080"
		default:
			c.Settings.WebSearch.BaseURL = "https://api.exa.ai"
		}
	}
	if c.Settings.WebSearch.APIKey == "" && c.Settings.WebSearch.APIKeyEnv != "" {
		c.Settings.WebSearch.APIKey = os.Getenv(c.Settings.WebSearch.APIKeyEnv)
	}
	if c.Settings.WebSearch.APIKeyEnv == "" && c.Settings.WebSearch.Provider == "exa" {
		c.Settings.WebSearch.APIKeyEnv = "EXA_API_KEY"
		c.Settings.WebSearch.APIKey = os.Getenv("EXA_API_KEY")
	}
	for id, role := range c.Roles {
		if role.ID == "" {
			role.ID = id
		}
		if role.Title == "" {
			role.Title = id
		}
		if role.Model == "" {
			role.Model = c.Settings.DefaultModel
		}
		if role.MaxTokens <= 0 {
			role.MaxTokens = c.Settings.MaxTokens
		}
		c.Roles[id] = role
	}
	for id, p := range c.Pipelines {
		if p.Settings.MaxParallel <= 0 {
			p.Settings.MaxParallel = c.Settings.MaxParallel
		}
		if p.Settings.MaxIterations <= 0 {
			p.Settings.MaxIterations = c.Settings.MaxIterations
		}
		if p.Settings.Workspace == "" {
			p.Settings.Workspace = c.Settings.Workspace
		}
		if p.Start == "" && len(p.Nodes) > 0 {
			p.Start = p.Nodes[0].ID
		}
		c.Pipelines[id] = p
	}
}

func looksLikePath(s string) bool {
	if strings.ContainsAny(s, "\n") {
		return false
	}
	lower := strings.ToLower(s)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt") ||
		strings.HasPrefix(s, "prompts/") || strings.HasPrefix(s, "./")
}

func resolvePath(baseDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// Validate checks structural consistency of the configuration.
func (c *Config) Validate() error {
	if len(c.Providers) == 0 {
		return fmt.Errorf("no providers configured")
	}
	for name, p := range c.Providers {
		if p.BaseURL == "" {
			return fmt.Errorf("provider %s: base_url is required", name)
		}
		if p.Type != "" && p.Type != "openai" {
			return fmt.Errorf("provider %s: unsupported type %q (only \"openai\")", name, p.Type)
		}
		if p.APIKeyEnv != "" && !validEnvName(p.APIKeyEnv) {
			return fmt.Errorf("provider %s: api_key_env %q must be an environment variable name, not the key itself; use api_key for a literal key", name, p.APIKeyEnv)
		}
	}
	for name, role := range c.Roles {
		if role.Model == "" && len(role.Fallback) == 0 {
			return fmt.Errorf("role %s: model is required", name)
		}
		for _, ref := range append([]string{role.Model}, role.Fallback...) {
			if ref == "" {
				continue
			}
			prov, _, err := SplitModel(ref)
			if err != nil {
				return fmt.Errorf("role %s: %w", name, err)
			}
			if _, ok := c.Providers[prov]; !ok {
				return fmt.Errorf("role %s: unknown provider %q in model %q", name, prov, ref)
			}
		}
	}
	for name, p := range c.Pipelines {
		if err := c.validatePipeline(name, p); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validatePipeline(name string, p Pipeline) error {
	ids := map[string]bool{}
	for _, n := range p.Nodes {
		if n.ID == "" {
			return fmt.Errorf("pipeline %s: node with empty id", name)
		}
		if ids[n.ID] {
			return fmt.Errorf("pipeline %s: duplicate node id %q", name, n.ID)
		}
		ids[n.ID] = true
		switch n.Type {
		case "agent", "parallel", "controller", "loop", "gate", "transform":
		default:
			return fmt.Errorf("pipeline %s: node %s: unknown type %q", name, n.ID, n.Type)
		}
		if n.Role != "" {
			if _, ok := c.Roles[n.Role]; !ok {
				return fmt.Errorf("pipeline %s: node %s: unknown role %q", name, n.ID, n.Role)
			}
		}
		for _, r := range n.Roles {
			if _, ok := c.Roles[r]; !ok {
				return fmt.Errorf("pipeline %s: node %s: unknown role %q", name, n.ID, r)
			}
		}
	}
	if len(p.Nodes) == 0 {
		return fmt.Errorf("pipeline %s: no nodes", name)
	}
	for _, e := range p.Edges {
		if !ids[e.From] {
			return fmt.Errorf("pipeline %s: edge from unknown node %q", name, e.From)
		}
		if !ids[e.To] {
			return fmt.Errorf("pipeline %s: edge to unknown node %q", name, e.To)
		}
	}
	for _, n := range p.Nodes {
		for _, next := range n.Next {
			if !ids[next] {
				return fmt.Errorf("pipeline %s: node %s: next references unknown node %q", name, n.ID, next)
			}
		}
		for _, b := range n.Body {
			if !ids[b] {
				return fmt.Errorf("pipeline %s: node %s: body references unknown node %q", name, n.ID, b)
			}
		}
		for _, c := range n.Choices {
			if !ids[c] {
				return fmt.Errorf("pipeline %s: node %s: choice %q is not a node", name, n.ID, c)
			}
		}
		if n.Else != "" && !ids[n.Else] {
			return fmt.Errorf("pipeline %s: node %s: else references unknown node %q", name, n.ID, n.Else)
		}
	}
	if p.Start != "" && !ids[p.Start] {
		return fmt.Errorf("pipeline %s: start references unknown node %q", name, p.Start)
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func floatPtr(f float64) *float64 { return &f }

// ContextLimit returns the configured context window for a "provider/model"
// reference, or 0 when unknown. It also accepts a bare model name.
func (c *Config) ContextLimit(ref string) int {
	if c.Settings.ModelLimits == nil {
		return 0
	}
	if v, ok := c.Settings.ModelLimits[ref]; ok {
		return v
	}
	if _, model, err := SplitModel(ref); err == nil {
		if v, ok := c.Settings.ModelLimits[model]; ok {
			return v
		}
	}
	return 0
}

// Price returns pricing for a "provider/model" reference, falling back to the
// bare model name.
func (c *Config) Price(ref string) (Price, bool) {
	if c.Settings.ModelPrices == nil {
		return Price{}, false
	}
	if p, ok := c.Settings.ModelPrices[ref]; ok {
		return p, true
	}
	if _, model, err := SplitModel(ref); err == nil {
		if p, ok := c.Settings.ModelPrices[model]; ok {
			return p, true
		}
	}
	return Price{}, false
}

// EstimateCost computes the USD cost of a token usage for a model reference.
func (c *Config) EstimateCost(ref string, promptTokens, completionTokens int) float64 {
	p, ok := c.Price(ref)
	if !ok {
		return 0
	}
	return float64(promptTokens)/1_000_000*p.InputPer1M +
		float64(completionTokens)/1_000_000*p.OutputPer1M
}

// SplitModel splits "provider/model" references.
func SplitModel(ref string) (provider, model string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid model reference %q (want provider/model)", ref)
	}
	return parts[0], parts[1], nil
}
