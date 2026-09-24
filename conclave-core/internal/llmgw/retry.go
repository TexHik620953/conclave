package llmgw

import (
	"context"
	"time"
)

// Retry wraps a Client with bounded exponential backoff for transient errors.
type Retry struct {
	Client    Client
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// Stream dispatches to the underlying client's Stream when available, otherwise
// falls back to Complete and emits the full content as a single delta.
func Stream(ctx context.Context, c Client, req Request, onDelta func(Delta)) (Response, error) {
	if sc, ok := c.(StreamingClient); ok {
		return sc.Stream(ctx, req, onDelta)
	}
	resp, err := c.Complete(ctx, req)
	if err == nil && onDelta != nil && resp.Message.Content != "" {
		onDelta(Delta{Content: resp.Message.Content})
	}
	return resp, err
}

// Stream implements StreamingClient with retries for transient errors before any
// delta is emitted.
func (r Retry) Stream(ctx context.Context, req Request, onDelta func(Delta)) (Response, error) {
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	base := r.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	max := r.MaxDelay
	if max <= 0 {
		max = 10 * time.Second
	}
	var last error
	for i := 0; i < attempts; i++ {
		emitted := false
		wrapped := func(d Delta) {
			emitted = true
			if onDelta != nil {
				onDelta(d)
			}
		}
		resp, err := Stream(ctx, r.Client, req, wrapped)
		if err == nil {
			return resp, nil
		}
		last = err
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		if emitted || i == attempts-1 {
			break
		}
		delay := base << i
		if delay > max {
			delay = max
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return Response{}, last
}

// Complete implements Client.
func (r Retry) Complete(ctx context.Context, req Request) (Response, error) {
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	base := r.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	max := r.MaxDelay
	if max <= 0 {
		max = 10 * time.Second
	}
	var last error
	for i := 0; i < attempts; i++ {
		resp, err := r.Client.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		last = err
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		if i == attempts-1 {
			break
		}
		delay := base << i
		if delay > max {
			delay = max
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return Response{}, last
}
