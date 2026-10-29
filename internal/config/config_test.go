package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedLoadResolveAndValidate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Seed(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, ".conclave", "config.yaml") {
		t.Fatalf("path = %s", path)
	}
	t.Setenv("DEEPSEEK_API_KEY", "deep-key")
	t.Setenv("OPENROUTER_API_KEY", "router-key")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["deepseek"].APIKey != "deep-key" {
		t.Fatalf("env expansion failed: %q", cfg.Providers["deepseek"].APIKey)
	}
	p, err := cfg.Profile("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Agent.Model == "" || len(p.Workers) == 0 {
		t.Fatalf("bad profile: %#v", p)
	}
	m, err := cfg.ResolveModel("openrouter/deepseek/deepseek-chat")
	if err != nil {
		t.Fatal(err)
	}
	if m.Provider != "openrouter" || m.ID != "deepseek/deepseek-chat" {
		t.Fatalf("resolved: %#v", m)
	}
}

func TestCycleRejectedByPlanValidation(t *testing.T) {
	// The cycle validator lives in conclave, but plan config must at least
	// reject references to unknown models.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ResolveModel("missing/thing"); err == nil {
		t.Fatal("expected an error")
	}
	_ = os.Remove(filepath.Join(dir, ".conclave", "config.yaml"))
}
