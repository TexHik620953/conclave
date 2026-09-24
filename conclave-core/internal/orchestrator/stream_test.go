package orchestrator

import (
	"context"
	"sync"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/stream"
	"github.com/texhik/conclave/conclave-core/internal/toolgw"
)

type recordingSink struct {
	mu     sync.Mutex
	deltas []stream.Delta
}

func (s *recordingSink) Emit(_ context.Context, _ string, d stream.Delta) {
	s.mu.Lock()
	s.deltas = append(s.deltas, d)
	s.mu.Unlock()
}

func TestRoleRunnerStreamsDeltas(t *testing.T) {
	client := &llmgw.Fake{StreamFn: func(_ llmgw.Request, onDelta func(llmgw.Delta)) llmgw.Response {
		onDelta(llmgw.Delta{Content: "Hel"})
		onDelta(llmgw.Delta{Content: "lo"})
		return llmgw.Response{Message: llmgw.Message{Role: "assistant", Content: "Hello"}}
	}}
	runner := &LLMRoleRunner{Client: client, DefaultModel: "m"}
	sink := &recordingSink{}
	ctx := stream.WithSink(context.Background(), "s", sink)

	out, err := runner.Run(ctx, RoleInput{Role: playbook.Role{Tier: "senior"}, Task: "x"})
	if err != nil || out != "Hello" {
		t.Fatalf("Run = %q err=%v", out, err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.deltas) != 3 {
		t.Fatalf("deltas = %d, want 3", len(sink.deltas))
	}
	if sink.deltas[0].Text != "Hel" || sink.deltas[1].Text != "lo" {
		t.Fatalf("unexpected deltas: %+v", sink.deltas)
	}
	final := sink.deltas[2]
	if !final.Final || final.Text != "Hello" || final.Role != "senior" {
		t.Fatalf("final delta = %+v", final)
	}
}

func TestStreamHelperFallsBackToComplete(t *testing.T) {
	client := &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		return llmgw.Response{Message: llmgw.Message{Role: "assistant", Content: "answer"}}
	}}
	var got []string
	resp, err := llmgw.Stream(context.Background(), client, llmgw.Request{}, func(d llmgw.Delta) {
		got = append(got, d.Content)
	})
	if err != nil || resp.Message.Content != "answer" {
		t.Fatalf("Stream = %+v err=%v", resp, err)
	}
	if len(got) != 1 || got[0] != "answer" {
		t.Fatalf("deltas = %v", got)
	}
}

func TestRoleRunnerInjectsInboxMessages(t *testing.T) {
	calls := 0
	var second llmgw.Request
	client := &llmgw.Fake{StreamFn: func(req llmgw.Request, _ func(llmgw.Delta)) llmgw.Response {
		calls++
		if calls == 1 {
			return llmgw.Response{Message: llmgw.Message{Role: "assistant",
				ToolCalls: []llmgw.ToolCall{{ID: "1", Name: "noop", Arguments: "{}"}}}}
		}
		second = req
		return llmgw.Response{Message: llmgw.Message{Role: "assistant", Content: "done"}}
	}}
	runner := &LLMRoleRunner{Client: client, Tools: &dynTools{}, DefaultModel: "m"}
	drained := false
	ctx := stream.WithInbox(context.Background(), func(context.Context) []string {
		if drained {
			return nil
		}
		drained = true
		return []string{"mid-run note"}
	})
	out, err := runner.Run(ctx, RoleInput{Role: playbook.Role{Tier: "senior"}, Task: "x"})
	if err != nil || out != "done" {
		t.Fatalf("Run = %q err=%v", out, err)
	}
	found := false
	for _, m := range second.Messages {
		if m.Role == "user" && m.Content == "mid-run note" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected message not found in %+v", second.Messages)
	}
}

type dynTools struct {
	defs []llmgw.ToolDef
}

func (d *dynTools) Execute(context.Context, toolgw.Call) (toolgw.Result, error) {
	return toolgw.Result{Content: "ok"}, nil
}

func (d *dynTools) ToolDefs(context.Context) []llmgw.ToolDef { return d.defs }

func TestRoleRunnerUsesDynamicToolDefs(t *testing.T) {
	var seen []llmgw.ToolDef
	client := &llmgw.Fake{StreamFn: func(req llmgw.Request, _ func(llmgw.Delta)) llmgw.Response {
		seen = req.Tools
		return llmgw.Response{Message: llmgw.Message{Role: "assistant", Content: "ok"}}
	}}
	runner := &LLMRoleRunner{Client: client, Tools: &dynTools{defs: []llmgw.ToolDef{{Name: "read_file"}}}, DefaultModel: "m"}
	if _, err := runner.Run(context.Background(), RoleInput{Role: playbook.Role{Tier: "senior"}, Task: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Name != "read_file" {
		t.Fatalf("tool defs = %+v", seen)
	}
}
