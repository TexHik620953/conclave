package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/provider"
)

func TestNodeToolOverride(t *testing.T) {
	var gotTools []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotTools = nil
		for _, tool := range body.Tools {
			gotTools = append(gotTools, tool.Function.Name)
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: srv.URL + "/v1"}},
		Roles:     map[string]config.Role{"researcher": {ID: "researcher", Model: "mock/m"}},
		Pipelines: map[string]config.Pipeline{
			"t": {Start: "research", Nodes: []config.Node{{
				ID: "research", Type: "agent", Role: "researcher",
				Tools: []string{"web_search", "http_fetch"}, Output: "research.md",
			}}},
		},
		Settings: config.Settings{MaxParallel: 1, MaxIterations: 2, Timeout: config.Duration(5 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Cfg: cfg,
		LLM: llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
	}
	if _, err := e.Run(context.Background(), RunOptions{Pipeline: "t", Task: "x"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"web_search": true, "http_fetch": true}
	if len(gotTools) != 2 || !want[gotTools[0]] || !want[gotTools[1]] {
		t.Fatalf("tools sent to model = %v, want [web_search http_fetch]", gotTools)
	}
}
