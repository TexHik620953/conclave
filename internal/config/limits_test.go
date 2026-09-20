package config

import "testing"

func TestEstimateCost(t *testing.T) {
	cfg := &Config{Settings: Settings{ModelPrices: map[string]Price{
		"openai/gpt-4o": {InputPer1M: 2.5, OutputPer1M: 10},
	}}}
	got := cfg.EstimateCost("openai/gpt-4o", 1_000_000, 500_000)
	want := 2.5 + 5.0
	if got != want {
		t.Fatalf("EstimateCost = %v, want %v", got, want)
	}
	if cfg.EstimateCost("unknown/model", 100, 100) != 0 {
		t.Fatal("unknown model should cost 0")
	}
}

func TestWebSearchAPIKeyResolution(t *testing.T) {
	t.Setenv("EXA_API_KEY", "env-key")

	literal := &Config{}
	literal.Settings.WebSearch.Provider = "exa"
	literal.Settings.WebSearch.APIKey = "literal-key"
	literal.applyDefaults()
	if literal.Settings.WebSearch.APIKey != "literal-key" {
		t.Fatalf("literal api_key was overwritten: %q", literal.Settings.WebSearch.APIKey)
	}

	fromEnv := &Config{}
	fromEnv.Settings.WebSearch.Provider = "exa"
	fromEnv.applyDefaults()
	if fromEnv.Settings.WebSearch.APIKey != "env-key" {
		t.Fatalf("env api_key not resolved: %q", fromEnv.Settings.WebSearch.APIKey)
	}
	if fromEnv.Settings.WebSearch.APIKeyEnv != "EXA_API_KEY" {
		t.Fatalf("api_key_env = %q", fromEnv.Settings.WebSearch.APIKeyEnv)
	}
}

func TestContextLimit(t *testing.T) {
	cfg := &Config{Settings: Settings{ModelLimits: map[string]int{
		"openai/gpt-4o":    128000,
		"qwen2.5-coder:7b": 32768,
	}}}
	cases := map[string]int{
		"openai/gpt-4o":           128000,
		"openai/gpt-4o-mini":      0,
		"ollama/qwen2.5-coder:7b": 32768,
		"gpt-4o":                  0,
	}
	for ref, want := range cases {
		if got := cfg.ContextLimit(ref); got != want {
			t.Errorf("ContextLimit(%q) = %d, want %d", ref, got, want)
		}
	}
}
