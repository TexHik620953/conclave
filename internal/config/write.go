package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// UpsertProvider writes a provider definition into the project config without
// touching the global config or other project settings.
func UpsertProvider(cwd, name string, p Provider) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("provider name is empty")
	}
	doc, path, err := readProjectDoc(cwd)
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	entry := map[string]any{}
	if old, ok := providers[name].(map[string]any); ok {
		entry = old
	}
	entry["base_url"] = p.BaseURL
	if p.APIKey != "" {
		entry["api_key"] = p.APIKey
	}
	if p.MaxParallel > 0 {
		entry["max_parallel"] = p.MaxParallel
	}
	if p.Models != nil {
		entry["models"] = modelsToMap(p.Models)
	}
	providers[name] = entry
	return writeProjectDoc(path, doc)
}

// UpsertModel adds or replaces one model under a provider in the project
// config. The provider itself may live in the global config.
func UpsertModel(cwd, provider, model string, m Model) error {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	doc, path, err := readProjectDoc(cwd)
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	entry := map[string]any{}
	if old, ok := providers[provider].(map[string]any); ok {
		entry = old
	}
	models := ensureMap(entry, "models")
	models[model] = map[string]any{
		"context":    m.Context,
		"max_output": m.MaxOutput,
		"capabilities": map[string]any{
			"tools":     m.Capabilities.Tools,
			"reasoning": m.Capabilities.Reasoning,
			"effort":    m.Capabilities.Effort,
		},
	}
	entry["models"] = models
	providers[provider] = entry
	return writeProjectDoc(path, doc)
}

func readProjectDoc(cwd string) (map[string]any, string, error) {
	path := ProjectPath(cwd)
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, path, err
		}
		return map[string]any{}, path, nil
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, path, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, path, nil
}

func writeProjectDoc(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	// The project config may contain a literal key entered through the TUI.
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if m, ok := parent[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

func modelsToMap(models map[string]Model) map[string]any {
	out := map[string]any{}
	for id, m := range models {
		out[id] = map[string]any{
			"context":    m.Context,
			"max_output": m.MaxOutput,
			"capabilities": map[string]any{
				"tools":     m.Capabilities.Tools,
				"reasoning": m.Capabilities.Reasoning,
				"effort":    m.Capabilities.Effort,
			},
		}
	}
	return out
}

// Global helpers -----------------------------------------------------------

func readGlobalDoc() (map[string]any, string, error) {
	path := GlobalPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, path, nil
		}
		return nil, path, err
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, path, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, path, nil
}

func writeGlobalDoc(doc map[string]any, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func UpsertGlobalProvider(name string, p Provider) error {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	entry := map[string]any{}
	if old, ok := providers[name].(map[string]any); ok {
		entry = old
	}
	entry["base_url"] = p.BaseURL
	if p.APIKey != "" {
		entry["api_key"] = p.APIKey
	}
	if p.MaxParallel > 0 {
		entry["max_parallel"] = p.MaxParallel
	}
	if p.Headers != nil {
		entry["headers"] = p.Headers
	}
	if p.Models != nil {
		entry["models"] = modelsToMap(p.Models)
	}
	providers[name] = entry
	return writeGlobalDoc(doc, path)
}

func UpsertGlobalModel(provider, model string, m Model) error {
	provider = strings.TrimSpace(strings.ToLower(provider))
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	entry := map[string]any{}
	if old, ok := providers[provider].(map[string]any); ok {
		entry = old
	}
	models := ensureMap(entry, "models")
	models[model] = map[string]any{
		"context":    m.Context,
		"max_output": m.MaxOutput,
		"capabilities": map[string]any{
			"tools":     m.Capabilities.Tools,
			"reasoning": m.Capabilities.Reasoning,
			"effort":    m.Capabilities.Effort,
		},
	}
	entry["models"] = models
	providers[provider] = entry
	return writeGlobalDoc(doc, path)
}

func SetGlobalKey(provider, key string) error {
	provider = strings.TrimSpace(strings.ToLower(provider))
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	entry, ok := providers[provider].(map[string]any)
	if !ok {
		return fmt.Errorf("provider %q not found", provider)
	}
	if strings.TrimSpace(key) == "" {
		delete(entry, "api_key")
	} else {
		entry["api_key"] = strings.TrimSpace(key)
	}
	providers[provider] = entry
	return writeGlobalDoc(doc, path)
}

func DeleteGlobalProvider(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	cfg, err := LoadGlobalOnly()
	if err != nil {
		return err
	}
	var fallback string
	used := false
	for pname, profile := range cfg.Profiles {
		refs := []string{profile.Agent.Model, profile.Master.Model}
		for _, w := range profile.Workers {
			refs = append(refs, w.Model)
		}
		for _, ref := range refs {
			if strings.HasPrefix(ref, name+"/") {
				used = true
				_ = pname
				break
			}
		}
	}
	if used {
		var candidates []string
		for pname, provider := range cfg.Providers {
			if pname == name {
				continue
			}
			for model := range provider.Models {
				candidates = append(candidates, pname+"/"+model)
			}
		}
		if len(candidates) == 0 {
			return fmt.Errorf("provider %q is used by profiles and there is no other configured model; add another provider/model first", name)
		}
		sort.Strings(candidates)
		fallback = candidates[0]
	}
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	delete(providers, name)
	if fallback != "" {
		profiles, _ := doc["profiles"].(map[string]any)
		for _, raw := range profiles {
			profile, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"agent", "master"} {
				entry, ok := profile[key].(map[string]any)
				if !ok {
					continue
				}
				if ref, _ := entry["model"].(string); strings.HasPrefix(ref, name+"/") {
					entry["model"] = fallback
				}
			}
			workers, _ := profile["workers"].([]any)
			for _, wr := range workers {
				worker, ok := wr.(map[string]any)
				if !ok {
					continue
				}
				if ref, _ := worker["model"].(string); strings.HasPrefix(ref, name+"/") {
					worker["model"] = fallback
				}
			}
		}
	}
	return writeGlobalDoc(doc, path)
}

func DeleteGlobalModel(provider, model string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	cfg, err := LoadGlobalOnly()
	if err == nil {
		ref := provider + "/" + model
		for pname, profile := range cfg.Profiles {
			refs := []string{profile.Agent.Model, profile.Master.Model}
			for _, w := range profile.Workers {
				refs = append(refs, w.Model)
			}
			for _, r := range refs {
				if r == ref {
					return fmt.Errorf("model %q is used by profile %q; switch it before deleting", ref, pname)
				}
			}
		}
	}
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	providers := ensureMap(doc, "providers")
	entry, ok := providers[strings.ToLower(provider)].(map[string]any)
	if !ok {
		return fmt.Errorf("provider %q not found", provider)
	}
	models := ensureMap(entry, "models")
	delete(models, model)
	entry["models"] = models
	providers[strings.ToLower(provider)] = entry
	return writeGlobalDoc(doc, path)
}

// LoadGlobalOnly loads just ~/.config/conclave/config.yaml.
func LoadGlobalOnly() (*Config, error) {
	cfg := Default()
	b, err := os.ReadFile(GlobalPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	cfg.Path = GlobalPath()
	cfg.expandEnv()
	return &cfg, nil
}

func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "••••"
	}
	return key[:4] + "••••" + key[len(key)-4:]
}

// MigrateProjectToGlobal moves provider keys and provider definitions from the
// project config into the global config and removes secrets from the project.
func MigrateProjectToGlobal(cwd string) error {
	projectPath := ProjectPath(cwd)
	pb, err := os.ReadFile(projectPath)
	if err != nil {
		return nil
	}
	pdoc := map[string]any{}
	if err := yaml.Unmarshal(pb, &pdoc); err != nil {
		return err
	}
	providersAny, _ := pdoc["providers"].(map[string]any)
	if len(providersAny) == 0 {
		return nil
	}
	gdoc, gpath, err := readGlobalDoc()
	if err != nil {
		return err
	}
	globalProviders := ensureMap(gdoc, "providers")
	changed := false
	dirtyProject := false
	for name, raw := range providersAny {
		pentry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		gentry, _ := globalProviders[name].(map[string]any)
		if gentry == nil {
			gentry = map[string]any{}
		}
		before := len(gentry)
		for k, v := range pentry {
			if k == "api_key" {
				if _, exists := gentry["api_key"]; !exists && v != nil {
					gentry[k] = v
					changed = true
				}
				delete(pentry, "api_key")
				dirtyProject = true
				continue
			}
			if _, exists := gentry[k]; !exists {
				gentry[k] = v
				changed = true
			}
		}
		if len(gentry) > before || globalProviders[name] == nil {
			globalProviders[name] = gentry
			changed = true
		}
		providersAny[name] = pentry
	}
	// Move profiles only when the global config has none.
	if _, ok := gdoc["profiles"]; !ok {
		if profiles, ok := pdoc["profiles"].(map[string]any); ok && len(profiles) > 0 {
			gdoc["profiles"] = profiles
			changed = true
		}
	}
	if _, ok := gdoc["default_profile"]; !ok {
		if dp, ok := pdoc["default_profile"]; ok {
			gdoc["default_profile"] = dp
			changed = true
		}
	}
	if changed {
		if err := writeGlobalDoc(gdoc, gpath); err != nil {
			return err
		}
	}
	if dirtyProject || len(providersAny) > 0 {
		// Providers and profiles are global now. Leaving them in the project
		// config would re-import deleted providers or stale profile models.
		delete(pdoc, "providers")
		delete(pdoc, "profiles")
		b, err := yaml.Marshal(pdoc)
		if err != nil {
			return err
		}
		_ = os.WriteFile(projectPath, b, 0o600)
	}
	return nil
}

func UpsertGlobalProfile(name string, profile Profile) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	profiles := ensureMap(doc, "profiles")
	// Round-trip through YAML to get the same shape as the config file.
	b, err := yaml.Marshal(map[string]any{"profile": profile})
	if err != nil {
		return err
	}
	var wrapper map[string]any
	if err := yaml.Unmarshal(b, &wrapper); err != nil {
		return err
	}
	entry, _ := wrapper["profile"].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	profiles[name] = entry
	return writeGlobalDoc(doc, path)
}

func DeleteGlobalProfile(name string) error {
	doc, path, err := readGlobalDoc()
	if err != nil {
		return err
	}
	profiles := ensureMap(doc, "profiles")
	delete(profiles, name)
	return writeGlobalDoc(doc, path)
}
