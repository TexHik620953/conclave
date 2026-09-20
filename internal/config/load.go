package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadOptions controls which configuration sources are read.
type LoadOptions struct {
	GlobalDir      string
	ProjectDir     string
	DisableGlobal  bool
	DisableProject bool
}

// DefaultGlobalDir returns ~/.config/conclave.
func DefaultGlobalDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "conclave")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".conclave"
	}
	return filepath.Join(home, ".config", "conclave")
}

// DefaultProjectDir returns ./.conclave.
func DefaultProjectDir() string { return ".conclave" }

// Load reads, merges and validates configuration from the global and project
// directories. Project values override global ones.
func Load(opts LoadOptions) (*Config, error) {
	if opts.GlobalDir == "" {
		opts.GlobalDir = DefaultGlobalDir()
	}
	if opts.ProjectDir == "" {
		opts.ProjectDir = DefaultProjectDir()
	}

	merged := map[string]any{}
	baseDir := ""
	dirs := []struct {
		dir     string
		enabled bool
	}{
		{opts.GlobalDir, !opts.DisableGlobal},
		{opts.ProjectDir, !opts.DisableProject},
	}
	for _, d := range dirs {
		if !d.enabled {
			continue
		}
		raw, err := loadDir(d.dir)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			continue
		}
		deepMerge(merged, raw)
		baseDir = d.dir
	}

	if len(merged) == 0 {
		return nil, fmt.Errorf("no configuration found in %s or %s", opts.GlobalDir, opts.ProjectDir)
	}

	interpolate(merged)
	blob, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(blob, &cfg); err != nil {
		return nil, fmt.Errorf("decoding merged config: %w", err)
	}
	cfg.BaseDir = baseDir
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// loadDir reads config.yaml plus roles/*.yaml and pipelines/*.yaml.
func loadDir(dir string) (map[string]any, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	out := map[string]any{}
	if m, err := decodeFile(filepath.Join(dir, "config.yaml")); err != nil {
		return nil, err
	} else if m != nil {
		out = m
	}
	if m, err := decodeDir(filepath.Join(dir, "roles")); err != nil {
		return nil, err
	} else if len(m) > 0 {
		roles := ensureMap(out, "roles")
		for k, v := range m {
			roles[k] = v
		}
	}
	if m, err := decodeDir(filepath.Join(dir, "pipelines")); err != nil {
		return nil, err
	} else if len(m) > 0 {
		pipelines := ensureMap(out, "pipelines")
		for k, v := range m {
			pipelines[k] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err := resolveDirPrompts(out, dir); err != nil {
		return nil, err
	}
	return out, nil
}

// resolveDirPrompts inlines prompt files relative to the directory that
// defined each role, so that global roles keep their global prompts when a
// project overrides other fields.
func resolveDirPrompts(raw map[string]any, dir string) error {
	roles, ok := raw["roles"].(map[string]any)
	if !ok {
		return nil
	}
	for id, entry := range roles {
		role, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		path, _ := role["prompt_file"].(string)
		if path == "" {
			if sp, ok := role["system_prompt"].(string); ok && looksLikePath(sp) {
				candidate := resolvePath(dir, sp)
				if fileExists(candidate) {
					path = sp
				}
			}
		}
		if path == "" {
			continue
		}
		full := resolvePath(dir, path)
		data, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("role %s: reading prompt file: %w", id, err)
		}
		role["system_prompt"] = string(data)
		role["prompt_file"] = full
		roles[id] = role
	}
	return nil
}

func decodeDir(dir string) (map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]any{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		m, err := decodeFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if m == nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ext)
		if id, ok := m["id"].(string); ok && id != "" {
			name = id
		}
		out[name] = m
	}
	return out, nil
}

func decodeFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if v, ok := parent[key].(map[string]any); ok {
		return v
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

// deepMerge recursively merges src into dst; src wins on scalar conflicts.
func deepMerge(dst, src map[string]any) {
	for k, sv := range src {
		if dv, ok := dst[k]; ok {
			dm, dok := dv.(map[string]any)
			sm, sok := sv.(map[string]any)
			if dok && sok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = sv
	}
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func expandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(match string) string {
		groups := envPattern.FindStringSubmatch(match)
		name := groups[1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		if len(groups) > 2 {
			return groups[2]
		}
		return ""
	})
}

func interpolate(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok {
				t[k] = expandEnv(s)
				continue
			}
			interpolate(val)
		}
	case []any:
		for i, val := range t {
			if s, ok := val.(string); ok {
				t[i] = expandEnv(s)
				continue
			}
			interpolate(val)
		}
	}
}
