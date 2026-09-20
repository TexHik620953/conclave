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
