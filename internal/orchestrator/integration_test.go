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
	"github.com/texhik/conclave/internal/store"
)

func mockProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
			Tools    []provider.Tool    `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		system := ""
		if len(body.Messages) > 0 {
			system = strings.ToLower(body.Messages[0].Content)
		}
		hasTodoResult := false
		for _, m := range body.Messages {
			if m.Role == "tool" && m.Name == "todo_write" {
				hasTodoResult = true
			}
		}
		message := provider.Message{Role: "assistant"}
		finish := "stop"
		switch {
		case strings.Contains(system, "pipeline controller"):
			if !hasTodoResult {
				args := `{"todos":[{"content":"plan","status":"completed"},{"content":"build","status":"in_progress"}]}`
				message.ToolCalls = []provider.ToolCall{{ID: "c1", Type: "function", Function: provider.FunctionCall{Name: "todo_write", Arguments: args}}}
				finish = "tool_calls"
			} else {
				message.Content = "implement"
			}
		case strings.Contains(system, "qa engineer"):
			message.Content = "Looks good.\nSTATUS: PASS"
		default:
			message.Content = "done"
		}
		resp := map[string]any{
			"id":    "1",
			"model": "m",
			"choices": []map[string]any{{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestIntegrationPipeline(t *testing.T) {
	srv := mockProvider(t)
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: srv.URL + "/v1"}},
		Roles: map[string]config.Role{
			"controller":  {ID: "controller", Title: "Controller", Model: "mock/m", SystemPrompt: "You are the pipeline controller."},
			"architect":   {ID: "architect", Title: "Architect", Model: "mock/m"},
			"senior":      {ID: "senior", Title: "Senior", Model: "mock/m"},
			"qa":          {ID: "qa", Title: "QA", Model: "mock/m", SystemPrompt: "You are a QA engineer."},
			"tech_writer": {ID: "tech_writer", Title: "Writer", Model: "mock/m"},
		},
		Pipelines: map[string]config.Pipeline{
			"t": {
				Start: "analyze",
				Nodes: []config.Node{
					{ID: "analyze", Type: "agent", Role: "architect", Output: "plan.md", Next: []string{"decide"}},
					{ID: "decide", Type: "controller", Role: "controller", Choices: []string{"implement", "stop"}},
					{ID: "implement", Type: "agent", Role: "senior", Output: "impl.md", Next: []string{"review"}},
					{ID: "review", Type: "parallel", Roles: []string{"qa"}, Output: "review.md", Next: []string{"summarize"}},
					{ID: "summarize", Type: "agent", Role: "tech_writer", Output: "summary.md"},
					{ID: "stop", Type: "transform", Template: "stopped"},
				},
			},
		},
		Settings: config.Settings{MaxParallel: 4, MaxIterations: 3, OnNoUser: "next", Timeout: config.Duration(10 * time.Second)},
	}

	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Cfg:   cfg,
		LLM:   llm.New(reg, 10*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		Store: st,
	}
	res, err := e.Run(context.Background(), RunOptions{Pipeline: "t", Task: "build it", Workspace: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	for _, name := range []string{"plan.md", "impl.md", "review.md", "summary.md"} {
		if _, ok := res.Artifacts[name]; !ok {
			t.Errorf("missing artifact %s; have %v", name, keys(res.Artifacts))
		}
	}
	if len(res.Todos) != 2 {
		t.Errorf("todos = %d, want 2", len(res.Todos))
	}
	if _, ok := res.Outputs["stop"]; ok {
		t.Errorf("stop branch should have been pruned")
	}
	nodes, err := st.ListNodes(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	prompts := map[string]string{}
	for _, n := range nodes {
		prompts[n.NodeID] = n.Prompt
	}
	if prompts["analyze"] == "" {
		t.Errorf("node prompt was not stored for analyze: %+v", prompts)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
