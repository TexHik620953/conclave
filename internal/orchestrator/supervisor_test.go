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

// TestSupervisorDelegation verifies that a supervisor node can delegate to a
// role and then finish, recording the delegated output.
func TestSupervisorDelegation(t *testing.T) {
	var delegated, finished bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
			Tools    []provider.Tool    `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		isController := false
		for _, tl := range body.Tools {
			if tl.Function.Name == "delegate" {
				isController = true
			}
		}
		hasDelegateResult := false
		for _, m := range body.Messages {
			if m.Role == "tool" && m.Name == "delegate" {
				hasDelegateResult = true
			}
		}

		message := provider.Message{Role: "assistant", Content: "ok"}
		finish := "stop"
		switch {
		case isController && !hasDelegateResult:
			message.Content = ""
			message.ToolCalls = []provider.ToolCall{{
				ID: "d1", Type: "function",
				Function: provider.FunctionCall{Name: "delegate", Arguments: `{"role":"architect","prompt":"plan it"}`},
			}}
			finish = "tool_calls"
		case isController:
			delegated = true
			message.Content = ""
			message.ToolCalls = []provider.ToolCall{{
				ID: "f1", Type: "function",
				Function: provider.FunctionCall{Name: "finish", Arguments: `{"summary":"completed the task"}`},
			}}
			finish = "tool_calls"
			finished = true
		default:
			message.Content = "the architect plan"
		}
		resp := map[string]any{
			"model": "m",
			"choices": []map[string]any{{
				"index": 0, "message": message, "finish_reason": finish,
			}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: srv.URL + "/v1"}},
		Roles: map[string]config.Role{
			"controller": {ID: "controller", Title: "Lead", Model: "mock/m", SystemPrompt: "You are the lead supervisor."},
			"architect":  {ID: "architect", Title: "Architect", Model: "mock/m"},
		},
		Pipelines: map[string]config.Pipeline{
			"auto": {Start: "lead", Nodes: []config.Node{{
				ID: "lead", Type: "supervisor", Role: "controller",
				Roles: []string{"architect"}, MaxSteps: 5, Output: "result.md",
			}}},
		},
		Settings: config.Settings{MaxIterations: 5, Timeout: config.Duration(10 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Cfg: cfg,
		LLM: llm.New(reg, 10*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
	}
	res, err := e.Run(context.Background(), RunOptions{Pipeline: "auto", Task: "build a thing", Workspace: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !delegated || !finished {
		t.Fatalf("delegated=%v finished=%v", delegated, finished)
	}
	if got := res.Outputs["lead"]; !strings.Contains(got, "completed the task") {
		t.Fatalf("supervisor output = %q", got)
	}
	if got := res.Outputs["lead.architect"]; got != "the architect plan" {
		t.Fatalf("delegated output = %q", got)
	}
	if _, ok := res.Artifacts["result.md"]; !ok {
		t.Fatalf("missing result.md artifact: %v", res.Artifacts)
	}
}
