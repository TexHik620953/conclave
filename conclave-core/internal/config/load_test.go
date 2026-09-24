package config

import "testing"

func TestProvidersFromEnv(t *testing.T) {
	t.Setenv("CORE_PROVIDERS", `{"openai":{"base_url":"https://api.openai.com/v1","api_key":"sk-abc"},"ollama":{"base_url":"http://localhost:11434/v1"}}`)
	t.Setenv("CORE_DEFAULT_PROVIDER", "ollama")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 2 {
		t.Fatalf("providers = %d", len(c.Providers))
	}
	if c.Providers["openai"].APIKey != "sk-abc" {
		t.Fatalf("api_key not parsed: %+v", c.Providers["openai"])
	}
	if c.DefaultProvider != "ollama" {
		t.Fatalf("default provider = %q", c.DefaultProvider)
	}
}

func TestLegacyLLMBecomesDefaultProvider(t *testing.T) {
	t.Setenv("CORE_PROVIDERS", "")
	t.Setenv("CORE_LLM_BASE_URL", "http://localhost:9999/v1")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Providers["default"]; !ok {
		t.Fatalf("expected default provider from legacy vars: %+v", c.Providers)
	}
}
