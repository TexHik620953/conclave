package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergeAndInterpolate(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	t.Setenv("TEST_KEY", "secret-value")

	write(t, global, "config.yaml", `
providers:
  mock:
    type: openai
    base_url: "http://localhost:1/v1"
    api_key_env: TEST_KEY
  other:
    type: openai
    base_url: "http://localhost:2/v1"
roles:
  architect:
    model: mock/one
    prompt_file: prompts/architect.md
    permissions:
      fs_write: true
`)
	write(t, global, "prompts/architect.md", "GLOBAL PROMPT")
	write(t, project, "config.yaml", `
settings:
  default_model: mock/two
roles:
  architect:
    model: mock/override
`)
	write(t, project, "roles/qa.yaml", `
id: qa
title: QA
model: mock/qa
prompt_file: prompts/qa.md
`)
	write(t, project, "prompts/qa.md", "QA PROMPT")

	cfg, err := Load(LoadOptions{GlobalDir: global, ProjectDir: project})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(cfg.Providers))
	}
	if got := cfg.Providers["mock"].APIKey; got != "secret-value" {
		t.Errorf("interpolated api key = %q, want secret-value", got)
	}
	if got := cfg.Roles["architect"].Model; got != "mock/override" {
		t.Errorf("project should override global model, got %q", got)
	}
	if got := cfg.Roles["architect"].SystemPrompt; got != "GLOBAL PROMPT" {
		t.Errorf("global prompt = %q", got)
	}
	if _, ok := cfg.Roles["qa"]; !ok {
		t.Errorf("role qa from roles/ dir missing")
	}
	if got := cfg.Roles["qa"].SystemPrompt; got != "QA PROMPT" {
		t.Errorf("qa prompt = %q", got)
	}
}

func TestValidateRejectsLiteralAPIKeyInEnvField(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.yaml", `
providers:
  deepseek:
    type: openai
    base_url: "https://api.deepseek.com/v1"
    api_key_env: "sk-abc123"
roles:
  a:
    model: deepseek/x
`)
	_, err := Load(LoadOptions{GlobalDir: dir, DisableProject: true})
	if err == nil {
		t.Fatal("expected validation error when api_key_env holds a literal key")
	}
	if !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "config.yaml", `
providers:
  mock:
    type: openai
    base_url: "http://localhost:1/v1"
roles:
  bad:
    model: nope/model
`)
	_, err := Load(LoadOptions{GlobalDir: dir, DisableProject: true})
	if err == nil {
		t.Fatal("expected validation error for unknown provider")
	}
}
