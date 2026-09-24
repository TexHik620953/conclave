// Package toolgw is the tool gateway: it executes internal tools and proxies
// external tools (fs/exec/git) to a local agent.
package toolgw

import (
	"context"
	"encoding/json"
	"sync"
)

// Call is a request to execute a tool.
type Call struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
	// IdempotencyKey makes retries after a reconnect safe.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Result is the outcome of a tool call.
type Result struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// Executor runs tool calls.
type Executor interface {
	Execute(ctx context.Context, call Call) (Result, error)
}

// Fake is a scriptable in-process Executor for tests.
type Fake struct {
	// Fn, when set, produces the result for each call.
	Fn func(call Call) Result

	mu    sync.Mutex
	calls []Call
}

// Execute implements Executor.
func (f *Fake) Execute(_ context.Context, call Call) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.Fn != nil {
		return f.Fn(call), nil
	}
	return Result{Content: "(fake tool output)"}, nil
}

// Calls returns a copy of the calls seen so far.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}
