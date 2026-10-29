package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUpsertProviderAndModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	if err := UpsertProvider(dir, "local", Provider{BaseURL: "http://localhost:1234/v1", APIKey: "secret", MaxParallel: 2}); err != nil {
		t.Fatal(err)
	}
	if err := UpsertModel(dir, "local", "my-model", Model{Context: 32000, MaxOutput: 4096, Capabilities: Capabilities{Tools: true}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".conclave", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %o", info.Mode().Perm())
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := cfg.ResolveModel("local/my-model")
	if err != nil {
		t.Fatal(err)
	}
	if m.Config.Context != 32000 || m.ProviderConfig.APIKey != "secret" {
		t.Fatalf("resolved = %#v", m)
	}
}

func TestMigrationRemovesProjectProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	// Put a literal key into the project config, mimicking an old workspace.
	path := ProjectPath(dir)
	b, _ := os.ReadFile(path)
	b = []byte(strings.ReplaceAll(string(b), "${OPENROUTER_API_KEY}", "sk-or-old-key"))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	project, _ := os.ReadFile(path)
	var projectDoc map[string]any
	if err := yaml.Unmarshal(project, &projectDoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := projectDoc["providers"]; ok {
		t.Fatalf("providers still present in project config:\n%s", project)
	}
	global, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(global), "sk-or-old-key") {
		t.Fatal("key was not migrated to global config")
	}
	if err := UpsertGlobalProvider("local", Provider{BaseURL: "http://localhost:1234/v1", Models: map[string]Model{"local-model": {Context: 32000, MaxOutput: 4096}}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGlobalProvider("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobalOnly()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["local"]; ok {
		t.Fatal("deleted provider was re-imported from project config")
	}
}

func TestDeleteLastProviderReassignsProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGlobalProvider("openrouter"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Fatal("openrouter still exists")
	}
	p := cfg.Profiles["default"]
	if !strings.HasPrefix(p.Agent.Model, "deepseek/") {
		t.Fatalf("agent model = %s", p.Agent.Model)
	}
	if !strings.HasPrefix(p.Master.Model, "deepseek/") {
		t.Fatalf("master model = %s", p.Master.Model)
	}
	for _, w := range p.Workers {
		if !strings.HasPrefix(w.Model, "deepseek/") {
			t.Fatalf("worker %s model = %s", w.ID, w.Model)
		}
	}
}
