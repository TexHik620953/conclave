package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
)

func TestStreamAssemblesToolCallsAndUsage(t *testing.T) {
	var sawParameters bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if len(tools) == 1 {
			fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
			_, sawParameters = fn["parameters"]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"hi"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2}}}`,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(line + "\n\n"))
		}
	}))
	defer srv.Close()
	c := NewOpenAI(config.Provider{BaseURL: srv.URL})
	model := Model{ID: "m", MaxOutput: 100}
	var text string
	var calls []domain.ToolCall
	var usage domain.Usage
	err := c.Stream(context.Background(), model, Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "go"}},
		Tools:    []Tool{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
	}, func(e Event) error {
		switch e.Type {
		case EventText:
			text += e.Text
		case EventTool:
			calls = append(calls, e.ToolCall)
		case EventUsage:
			usage = e.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawParameters {
		t.Fatal("tool parameters were not sent as an object")
	}
	if text != "hi" {
		t.Fatalf("text = %q", text)
	}
	if len(calls) != 1 || calls[0].Name != "read_file" || !strings.Contains(calls[0].Arguments, `"x"`) {
		t.Fatalf("calls = %#v", calls)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestMessagesAlwaysCarryContentField(t *testing.T) {
	c := NewOpenAI(config.Provider{BaseURL: "http://example.com"})
	body, err := c.buildRequest(Model{ID: "m", MaxOutput: 100}, Request{
		Messages: []domain.Message{
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}}},
			{Role: domain.RoleTool, ToolCallID: "c1", Content: ""},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for i, m := range payload.Messages {
		if _, ok := m["content"]; !ok {
			t.Fatalf("message %d has no content field: %#v", i, m)
		}
	}
}

func TestCompletionLimitIsCapped(t *testing.T) {
	c := NewOpenAI(config.Provider{BaseURL: "http://example.com"})
	body, err := c.buildRequest(Model{ID: "m", MaxOutput: 1_000_000, Context: 128000}, Request{Messages: []domain.Message{{Role: domain.RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MaxTokens > 32768 || payload.MaxTokens <= 0 {
		t.Fatalf("max_tokens = %d", payload.MaxTokens)
	}
}
