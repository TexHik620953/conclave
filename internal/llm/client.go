// Package llm provides a high-level chat interface with model fallback.
package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/texhik/conclave/internal/provider"
)

// RetryPolicy controls retries for transient provider errors.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Client performs chat completions with fallback across models.
type Client struct {
	registry *provider.Registry
	timeout  time.Duration
	retry    RetryPolicy
}

// New creates a client over the provider registry.
func New(registry *provider.Registry, timeout time.Duration, retry RetryPolicy) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if retry.MaxAttempts <= 0 {
		retry.MaxAttempts = 3
	}
	if retry.BaseDelay <= 0 {
		retry.BaseDelay = time.Second
	}
	if retry.MaxDelay <= 0 {
		retry.MaxDelay = 30 * time.Second
	}
	return &Client{registry: registry, timeout: timeout, retry: retry}
}

// Registry exposes the underlying provider registry.
func (c *Client) Registry() *provider.Registry { return c.registry }

// Request describes a completion request.
type Request struct {
	Model       string
	Fallback    []string
	Messages    []provider.Message
	Tools       []provider.Tool
	ToolChoice  any
	Temperature *float64
	MaxTokens   int
	OnDelta     func(provider.Delta)
}

// Response is a completed assistant turn.
type Response struct {
	Message      provider.Message
	FinishReason string
	Usage        provider.Usage
	Model        string
	Provider     string
}

// Complete runs the request, trying each model target in order until one
// succeeds.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	targets := make([]string, 0, 1+len(req.Fallback))
	if req.Model != "" {
		targets = append(targets, req.Model)
	}
	targets = append(targets, req.Fallback...)
	if len(targets) == 0 {
		return nil, fmt.Errorf("llm: no model specified")
	}

	// Once any content has been streamed to the caller, falling back to another
	// model would duplicate or interleave output, so we stop instead.
	emitted := false
	if req.OnDelta != nil {
		inner := req.OnDelta
		req.OnDelta = func(d provider.Delta) {
			emitted = true
			inner(d)
		}
	}

	var lastErr error
	for i, ref := range targets {
		target, err := provider.Resolve(ref)
		if err != nil {
			lastErr = err
			continue
		}
		client, err := c.registry.Client(target.Provider)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := c.callTarget(ctx, client, target, req)
		if err == nil {
			choice := resp.Choices[0]
			return &Response{
				Message:      choice.Message,
				FinishReason: choice.FinishReason,
				Usage:        resp.Usage,
				Model:        target.Model,
				Provider:     target.Provider,
			}, nil
		}
		lastErr = fmt.Errorf("%s: %w", ref, err)
		if emitted {
			return nil, fmt.Errorf("llm: streaming failed after partial output: %w", lastErr)
		}
		if i < len(targets)-1 {
			continue
		}
	}
	return nil, fmt.Errorf("llm: all model targets failed: %w", lastErr)
}

// callTarget performs one model call with retries for transient errors.
func (c *Client) callTarget(ctx context.Context, client *provider.Client, target provider.Target, req Request) (*provider.ChatResponse, error) {
	creq := provider.ChatRequest{
		Model:       target.Model,
		Messages:    req.Messages,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	var lastErr error
	for attempt := 1; attempt <= c.retry.MaxAttempts; attempt++ {
		emitted := false
		var onDelta func(provider.Delta)
		if req.OnDelta != nil {
			onDelta = func(d provider.Delta) {
				emitted = true
				req.OnDelta(d)
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		var resp *provider.ChatResponse
		var err error
		if onDelta != nil {
			resp, err = client.ChatStream(callCtx, creq, onDelta)
		} else {
			resp, err = client.Chat(callCtx, creq)
		}
		cancel()
		if err == nil {
			if len(resp.Choices) == 0 {
				err = fmt.Errorf("empty response")
			} else {
				return resp, nil
			}
		}
		lastErr = err
		if !retryable(ctx, err) || emitted || attempt == c.retry.MaxAttempts {
			break
		}
		delay := c.backoff(attempt, provider.RetryAfterOf(err))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

func retryable(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ctx.Err() == nil
	}
	var apiErr *provider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	return true
}

func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > c.retry.MaxDelay {
			return c.retry.MaxDelay
		}
		return retryAfter
	}
	delay := float64(c.retry.BaseDelay) * math.Pow(2, float64(attempt-1))
	if max := float64(c.retry.MaxDelay); delay > max {
		delay = max
	}
	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(delay * jitter)
}
