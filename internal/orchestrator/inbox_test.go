package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/provider"
)

// TestInFlightMessageInjection verifies that a message queued while a run is
// active is injected into the role's context at the next iteration.
func TestInFlightMessageInjection(t *testing.T) {
	var sawInjection bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			if strings.Contains(m.Content, "Message from the user") && strings.Contains(m.Content, "change of plan") {
				sawInjection = true
			}
		}
		resp := map[string]any{
			"model": "m",
			"choices": []map[string]any{{
				"index":         0,
				"message":       provider.Message{Role: "assistant", Content: "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: srv.URL + "/v1"}},
		Roles:     map[string]config.Role{"senior": {ID: "senior", Title: "Senior", Model: "mock/m"}},
		Pipelines: map[string]config.Pipeline{
			"t": {Start: "work", Nodes: []config.Node{{ID: "work", Type: "agent", Role: "senior", Output: "out.md"}}},
		},
		Settings: config.Settings{MaxIterations: 2, Timeout: config.Duration(10 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Cfg: cfg,
		LLM: llm.New(reg, 10*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
	}
	delivered := false
	e.Inbox = func(string) []string {
		if delivered {
			return nil
		}
		delivered = true
		return []string{"change of plan"}
	}
	if _, err := e.Run(context.Background(), RunOptions{Pipeline: "t", Task: "do it", Workspace: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawInjection {
		t.Fatal("expected the in-flight message to be injected into the role context")
	}
}
