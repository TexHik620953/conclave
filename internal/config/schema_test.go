package config

import (
	"encoding/json"
	"testing"
)

func TestSchemaIsValidJSON(t *testing.T) {
	data, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	for _, key := range []string{"providers", "roles", "pipelines", "settings", "mcp"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing %q", key)
		}
	}
}
