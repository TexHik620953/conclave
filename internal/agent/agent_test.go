package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
)

type fakeClient struct {
	calls int
}

func (f *fakeClient) Stream(ctx context.Context, m llm.Model, req llm.Request, sink func(llm.Event) error) error {
	f.calls++
	if f.calls == 1 {
		_ = sink(llm.Event{Type: llm.EventTool, ToolCall: domain.ToolCall{
			ID: "c1", Name: "write_file", Arguments: `{"path":"hello.txt","content":"hello"}`,
		}})
	} else {
		_ = sink(llm.Event{Type: llm.EventText, Text: "done"})
	}
	_ = sink(llm.Event{Type: llm.EventUsage, Usage: domain.Usage{PromptTokens: 12, CompletionTokens: 2}})
	return sink(llm.Event{Type: llm.EventDone})
}

func TestRuntimeExecutesToolLoop(t *testing.T) {
	dir := t.TempDir()
	rt := New(Config{
		AgentID: "test", Model: llm.Model{Ref: "fake/model", Provider: "fake", ID: "model", Context: 1000},
		Client: &fakeClient{}, System: "test", Cwd: dir, Mode: domain.ModeBuild,
		Tools: tools.Builtins(),
	})
	res, err := rt.Run(context.Background(), []domain.Message{{Role: domain.RoleUser, Content: "write"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Fatalf("file = %q", b)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("messages = %d, want assistant/tool/assistant", len(res.Messages))
	}
	if len(res.Records) != 2 || res.Records[0].Provider != "fake" || res.Records[0].InputTokens != 12 {
		t.Fatalf("usage records = %#v", res.Records)
	}
}

type batchClient struct{ calls int }

func (f *batchClient) Stream(ctx context.Context, m llm.Model, req llm.Request, sink func(llm.Event) error) error {
	f.calls++
	if f.calls == 1 {
		for _, id := range []string{"a", "b"} {
			_ = sink(llm.Event{Type: llm.EventTool, ToolCall: domain.ToolCall{ID: id, Name: "read_file", Arguments: `{"path":"x"}`}})
		}
	} else {
		_ = sink(llm.Event{Type: llm.EventText, Text: "done"})
	}
	_ = sink(llm.Event{Type: llm.EventUsage, Usage: domain.Usage{PromptTokens: 1, CompletionTokens: 1}})
	return sink(llm.Event{Type: llm.EventDone})
}

type slowRead struct {
	active int32
	max    int32
}

func (s *slowRead) Name() string   { return "read_file" }
func (s *slowRead) Spec() llm.Tool { return llm.Tool{Name: "read_file", Parameters: map[string]any{}} }
func (s *slowRead) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	n := atomic.AddInt32(&s.active, 1)
	for {
		old := atomic.LoadInt32(&s.max)
		if n <= old || atomic.CompareAndSwapInt32(&s.max, old, n) {
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	atomic.AddInt32(&s.active, -1)
	return "ok", nil
}

func TestParallelReadOnlyToolBatch(t *testing.T) {
	tool := &slowRead{}
	reg := tools.NewRegistry()
	reg.Add(tool)
	rt := New(Config{AgentID: "x", Model: llm.Model{Ref: "f/m", ID: "m"}, Client: &batchClient{}, Cwd: t.TempDir(), Mode: domain.ModeBuild, Tools: reg})
	start := time.Now()
	_, err := rt.Run(context.Background(), []domain.Message{{Role: domain.RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 180*time.Millisecond {
		t.Fatalf("tools were not parallel: %s", time.Since(start))
	}
	if atomic.LoadInt32(&tool.max) < 2 {
		t.Fatal("max concurrency was 1")
	}
}
