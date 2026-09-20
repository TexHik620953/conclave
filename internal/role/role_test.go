package role

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/tool"
)

func TestEventsCarryContextAndToolResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []provider.Message `json:"messages"`
			Stream   bool               `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		hasTool := false
		for _, m := range body.Messages {
			if m.Role == "tool" {
				hasTool = true
			}
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			if hasTool {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"final\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"total_tokens\":9}}\n\n")
			} else {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"hello.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":9,\"total_tokens\":9}}\n\n")
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if hasTool {
			fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"final"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"total_tokens":9}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"total_tokens":9}}`)
	}))
	defer srv.Close()

	reg, err := provider.NewRegistry(map[string]config.Provider{
		"mock": {Type: "openai", BaseURL: srv.URL + "/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	ch, cancel := bus.Subscribe(64)
	defer cancel()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi there"), 0o644)
	env := &tool.Env{Workspace: dir, FSRead: true}
	rt := &Runtime{
		Def:      config.Role{ID: "qa", SystemPrompt: "sys"},
		LLM:      llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		Tools:    tool.NewRegistry(env),
		Bus:      bus,
		RunID:    "r1",
		NodeID:   "n1",
		Model:    "mock/m",
		Fallback: nil,
	}
	if _, err := rt.Run(context.Background(), Input{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}

	var sawMessage, sawCall, sawResult bool
	deadline := time.After(2 * time.Second)
	for !(sawMessage && sawCall && sawResult) {
		select {
		case ev := <-ch:
			if ev.NodeID != "n1" || ev.Role != "qa" {
				continue
			}
			switch ev.Type {
			case event.RoleMessage:
				if ev.Model == "" {
					t.Fatal("role.message missing model")
				}
				sawMessage = true
			case event.ToolCall:
				if ev.Data["id"] != "call_1" || ev.Data["name"] != "read_file" || ev.Data["arguments"] == "" {
					t.Fatalf("tool.call missing fields: %+v", ev.Data)
				}
				sawCall = true
			case event.ToolResult:
				if ev.Data["content"] == nil || ev.Data["content"] == "" {
					t.Fatalf("tool.result missing content: %+v", ev.Data)
				}
				sawResult = true
			}
		case <-deadline:
			t.Fatalf("missing events: message=%v call=%v result=%v", sawMessage, sawCall, sawResult)
		}
	}
}

func TestCompactSummarizesLongHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"SUMMARY"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	reg, err := provider.NewRegistry(map[string]config.Provider{
		"mock": {Type: "openai", BaseURL: srv.URL + "/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{
		LLM:                llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		Model:              "mock/m",
		ContextLimit:       100,
		SummarizeThreshold: 0.5,
	}
	msgs := []provider.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, provider.Message{Role: "user", Content: strings.Repeat("x", 40)})
	}
	got := rt.compact(context.Background(), msgs)
	if len(got) >= len(msgs) {
		t.Fatalf("expected compaction, len %d -> %d", len(msgs), len(got))
	}
	if got[0].Role != "system" {
		t.Fatalf("system message not preserved: %+v", got[0])
	}
	found := false
	for _, m := range got {
		if strings.Contains(m.Content, "SUMMARY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary not inserted: %+v", got)
	}
}

func TestCompactDisabledBelowThreshold(t *testing.T) {
	rt := &Runtime{ContextLimit: 100000, SummarizeThreshold: 0.8}
	msgs := []provider.Message{{Role: "user", Content: "short"}}
	if got := rt.compact(context.Background(), msgs); len(got) != len(msgs) {
		t.Fatal("should not compact below threshold")
	}
}
