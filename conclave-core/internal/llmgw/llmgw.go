// Package llmgw is the LLM gateway: a provider-agnostic completion interface.
package llmgw

import (
	"context"
	"encoding/json"
)

// Message is one chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolDef declares a tool the model may call.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Usage reports token consumption.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Request is a completion request.
type Request struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// Response is a completion result.
type Response struct {
	Message Message `json:"message"`
	Usage   Usage   `json:"usage"`
}

// Delta is a streamed chunk of an assistant response.
type Delta struct {
	Content   string
	Reasoning string
}

// Client performs completions.
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamingClient additionally streams deltas as they are generated. Clients
// that do not implement it fall back to Complete (no incremental output).
type StreamingClient interface {
	Client
	Stream(ctx context.Context, req Request, onDelta func(Delta)) (Response, error)
}

// ModelResolver returns the model reference for a named top-level brain
// (controller, planner, interviewer, critic, supervisor).
type ModelResolver interface {
	BrainModel(name string) string
}
