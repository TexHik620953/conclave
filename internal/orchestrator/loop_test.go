package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/provider"
)

func TestResearchLoop(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		system := ""
		if len(body.Messages) > 0 {
			system = strings.ToLower(body.Messages[0].Content)
		}
		content := "findings"
		mu.Lock()
		switch {
		case strings.Contains(system, "critic"):
			counts["critic"]++
			if counts["critic"] == 1 {
				content = "needs more.\nSTATUS: MORE"
			} else {
				content = "sufficient.\nSTATUS: DONE"
			}
		case strings.Contains(system, "writer"):
			counts["writer"]++
			content = "brief"
		default:
			counts["researcher"]++
		}
		mu.Unlock()
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: srv.URL + "/v1"}},
		Roles: map[string]config.Role{
			"researcher": {ID: "researcher", Model: "mock/m", SystemPrompt: "You are a researcher."},
			"critic":     {ID: "critic", Model: "mock/m", SystemPrompt: "You are a critic."},
			"writer":     {ID: "writer", Model: "mock/m", SystemPrompt: "You are a writer."},
		},
		Pipelines: map[string]config.Pipeline{
			"t": {
				Start: "iterate",
				Nodes: []config.Node{
					{ID: "iterate", Type: "loop", Until: `critique contains "STATUS: DONE"`, MaxIter: 3, Body: []string{"research", "critique"}, Next: []string{"brief"}},
					{ID: "research", Type: "agent", Role: "researcher", Output: "research.md"},
					{ID: "critique", Type: "agent", Role: "critic", Output: "critique.md"},
					{ID: "brief", Type: "agent", Role: "writer", Output: "brief.md"},
				},
			},
		},
		Settings: config.Settings{MaxParallel: 1, MaxIterations: 3, Timeout: config.Duration(5 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Cfg: cfg,
		LLM: llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
	}
	res, err := e.Run(context.Background(), RunOptions{Pipeline: "t", Task: "research X"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["researcher"] != 2 {
		t.Errorf("researcher calls = %d, want 2", counts["researcher"])
	}
	if counts["critic"] != 2 {
		t.Errorf("critic calls = %d, want 2", counts["critic"])
	}
	if counts["writer"] != 1 {
		t.Errorf("writer calls = %d, want 1", counts["writer"])
	}
	if res.Outputs["brief"] != "brief" {
		t.Errorf("brief = %q", res.Outputs["brief"])
	}
}
