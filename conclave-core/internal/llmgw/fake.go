package llmgw

import (
	"context"
	"sync"
)

// Fake is a scriptable in-process Client for tests and eval.
type Fake struct {
	// Fn, when set, produces the response for each request.
	Fn func(req Request) Response
	// StreamFn, when set, produces a streamed response. It should invoke onDelta
	// for each chunk. When nil, Stream falls back to Fn/Complete as one delta.
	StreamFn func(req Request, onDelta func(Delta)) Response

	mu    sync.Mutex
	calls []Request
}

// Complete implements Client.
func (f *Fake) Complete(_ context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.Fn != nil {
		return f.Fn(req), nil
	}
	return Response{Message: Message{Role: "assistant", Content: "ok"}, Usage: Usage{PromptTokens: 1, CompletionTokens: 1}}, nil
}

// Stream implements StreamingClient.
func (f *Fake) Stream(_ context.Context, req Request, onDelta func(Delta)) (Response, error) {
	if f.StreamFn != nil {
		f.mu.Lock()
		f.calls = append(f.calls, req)
		f.mu.Unlock()
		return f.StreamFn(req, onDelta), nil
	}
	resp, err := f.Complete(context.Background(), req)
	if err != nil {
		return resp, err
	}
	if onDelta != nil && resp.Message.Content != "" {
		onDelta(Delta{Content: resp.Message.Content})
	}
	return resp, nil
}

// Calls returns a copy of the requests seen so far.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.calls))
	copy(out, f.calls)
	return out
}
