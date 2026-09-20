// Package role implements role definitions and the per-role agent loop.
package role

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/tool"
)

// Defaults are the process-wide permission defaults applied when a role leaves
// a permission unset.
type Defaults struct {
	FSRead          bool
	FSWrite         bool
	Shell           bool
	Network         bool
	AllowedHosts    []string
	AllowedCommands []string
	DeniedCommands  []string
	CommandTimeout  time.Duration
	MaxOutputBytes  int
	WebSearch       *tool.WebSearchConfig
}

// Runtime executes a single role.
type Runtime struct {
	Def           config.Role
	LLM           *llm.Client
	Tools         *tool.Registry
	MaxIterations int
	Bus           *event.Bus
	RunID         string
	NodeID        string
	Model         string
	Fallback      []string
	ContextLimit  int
	OnDelta       func(provider.Delta)
	BeforeCall    func() error
	AfterCall     func(providerName, model string, usage provider.Usage)

	SummarizeThreshold float64
	SummarizerModel    string
}

// Input is a request to a role.
type Input struct {
	Messages []provider.Message
	Prompt   string
	Context  string
}

// Result is a completed role turn.
type Result struct {
	Content    string
	Messages   []provider.Message
	Usage      provider.Usage
	ToolCalls  int
	Iterations int
	Model      string
	Provider   string

	ContextTokens int
	ContextLimit  int
}

// BuildEnv constructs the sandbox environment for a role.
func BuildEnv(role config.Role, workspace string, d Defaults) *tool.Env {
	return &tool.Env{
		Workspace:       workspace,
		FSRead:          role.Permissions.Allowed("fs_read", d.FSRead),
		FSWrite:         role.Permissions.Allowed("fs_write", d.FSWrite),
		Shell:           role.Permissions.Allowed("shell", d.Shell),
		Network:         role.Permissions.Allowed("network", d.Network),
		AllowedHosts:    d.AllowedHosts,
		AllowedCommands: d.AllowedCommands,
		DeniedCommands:  d.DeniedCommands,
		CommandTimeout:  d.CommandTimeout,
		MaxOutputBytes:  d.MaxOutputBytes,
		WebSearch:       d.WebSearch,
	}
}

// Run executes the role's agent loop until it produces a final answer or
// reaches the iteration limit.
func (r *Runtime) Run(ctx context.Context, in Input) (*Result, error) {
	system := strings.TrimSpace(r.Def.SystemPrompt)
	if in.Context != "" {
		system = strings.TrimSpace(system + "\n\n# Context\n" + in.Context)
	}
	messages := make([]provider.Message, 0, len(in.Messages)+2)
	if system != "" {
		messages = append(messages, provider.Message{Role: "system", Content: system})
	}
	messages = append(messages, in.Messages...)
	if in.Prompt != "" {
		messages = append(messages, provider.Message{Role: "user", Content: in.Prompt})
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("role %s: no input", r.Def.ID)
	}

	var defs []provider.Tool
	if r.Tools != nil {
		defs = r.Tools.Definitions()
	}

	maxIter := r.MaxIterations
	if maxIter <= 0 {
		maxIter = 8
	}
	result := &Result{Model: r.Model}
	for i := 0; i < maxIter; i++ {
		result.Iterations = i + 1
		if r.BeforeCall != nil {
			if err := r.BeforeCall(); err != nil {
				return nil, err
			}
		}
		messages = r.compact(ctx, messages)
		onDelta := r.OnDelta
		if r.Bus != nil {
			bus := r.Bus
			prev := onDelta
			onDelta = func(d provider.Delta) {
				if prev != nil {
					prev(d)
				}
				if d.Content != "" {
					bus.Publish(r.event(event.RoleDelta, r.Model, d.Content, nil))
				}
			}
		}
		resp, err := r.LLM.Complete(ctx, llm.Request{
			Model:       r.Model,
			Fallback:    r.Fallback,
			Messages:    messages,
			Tools:       defs,
			Temperature: r.Def.Temperature,
			MaxTokens:   r.Def.MaxTokens,
			OnDelta:     onDelta,
		})
		if err != nil {
			return nil, err
		}
		result.Model = resp.Model
		result.Provider = resp.Provider
		addUsage(&result.Usage, resp.Usage)
		if r.AfterCall != nil {
			r.AfterCall(resp.Provider, resp.Model, resp.Usage)
		}

		ctxTokens := resp.Usage.PromptTokens
		if ctxTokens == 0 {
			ctxTokens = estimateTokens(messages)
		}
		result.ContextTokens = ctxTokens
		result.ContextLimit = r.ContextLimit
		if r.Bus != nil {
			r.Bus.Publish(r.event(event.ContextUsage, resp.Model, "", map[string]any{
				"prompt_tokens":     ctxTokens,
				"completion_tokens": resp.Usage.CompletionTokens,
				"total_tokens":      resp.Usage.TotalTokens,
				"context_limit":     r.ContextLimit,
				"messages":          len(messages),
			}))
		}

		assistant := resp.Message
		assistant.Role = "assistant"
		messages = append(messages, assistant)
		if assistant.Content != "" {
			result.Content = assistant.Content
			if r.Bus != nil {
				r.Bus.Publish(r.event(event.RoleMessage, resp.Model, assistant.Content, nil))
			}
		}
		if len(assistant.ToolCalls) == 0 {
			result.Messages = messages
			return result, nil
		}
		for _, tc := range assistant.ToolCalls {
			result.ToolCalls++
			args := json.RawMessage(tc.Function.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			if r.Bus != nil {
				r.Bus.Publish(r.event(event.ToolCall, resp.Model, tc.Function.Name, map[string]any{
					"id":        tc.ID,
					"name":      tc.Function.Name,
					"arguments": string(args),
				}))
			}
			var res tool.Result
			if r.Tools != nil {
				res, _ = r.Tools.Execute(ctx, tc.Function.Name, args)
			} else {
				res = tool.Result{Content: "no tools available", IsError: true}
			}
			if r.Bus != nil {
				r.Bus.Publish(r.event(event.ToolResult, resp.Model, tc.Function.Name, map[string]any{
					"id":       tc.ID,
					"name":     tc.Function.Name,
					"is_error": res.IsError,
					"content":  truncateForEvent(res.Content),
				}))
			}
			messages = append(messages, provider.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    res.Content,
			})
		}
	}
	result.Messages = messages
	if strings.TrimSpace(result.Content) == "" {
		// Force a final, tool-free answer so the pipeline can continue.
		finalMsgs := append(append([]provider.Message{}, messages...), provider.Message{
			Role:    "user",
			Content: "Stop using tools and give your final answer now, based on the information gathered.",
		})
		if resp, err := r.LLM.Complete(ctx, llm.Request{
			Model:       r.Model,
			Fallback:    r.Fallback,
			Messages:    finalMsgs,
			Temperature: r.Def.Temperature,
			MaxTokens:   r.Def.MaxTokens,
		}); err == nil && strings.TrimSpace(resp.Message.Content) != "" {
			result.Content = resp.Message.Content
			result.Model = resp.Model
			result.Provider = resp.Provider
			addUsage(&result.Usage, resp.Usage)
			if r.AfterCall != nil {
				r.AfterCall(resp.Provider, resp.Model, resp.Usage)
			}
			messages = append(messages, provider.Message{Role: "assistant", Content: result.Content})
		}
	}
	result.Messages = messages
	if strings.TrimSpace(result.Content) != "" {
		if r.Bus != nil {
			r.Bus.Publish(r.event(event.Error, result.Model,
				fmt.Sprintf("reached %d iterations; returned a final answer", maxIter), nil))
		}
		return result, nil
	}
	return result, fmt.Errorf("role %s: exceeded %d iterations without a final answer", r.Def.ID, maxIter)
}

// event builds a role event carrying run, node, role and model context.
func (r *Runtime) event(t event.Type, model, message string, data map[string]any) event.Event {
	return event.Event{
		Type:    t,
		RunID:   r.RunID,
		NodeID:  r.NodeID,
		Role:    r.Def.ID,
		Model:   model,
		Message: message,
		Data:    data,
	}
}

// compact summarizes older messages when the estimated prompt size approaches
// the model's context limit. It preserves the system message and the most
// recent exchanges, and never splits a tool call from its result.
func (r *Runtime) compact(ctx context.Context, messages []provider.Message) []provider.Message {
	if r.SummarizeThreshold <= 0 || r.ContextLimit <= 0 || r.LLM == nil {
		return messages
	}
	est := estimateTokens(messages)
	if float64(est) < r.SummarizeThreshold*float64(r.ContextLimit) {
		return messages
	}
	const keep = 6
	if len(messages) <= keep+1 {
		return messages
	}
	start := 0
	if messages[0].Role == "system" {
		start = 1
	}
	cut := len(messages) - keep
	if cut <= start {
		return messages
	}
	for cut < len(messages) && messages[cut].Role == "tool" {
		cut++
	}
	if cut >= len(messages) {
		return messages
	}
	middle := messages[start:cut]
	summary, err := r.summarize(ctx, middle)
	if err != nil || summary == "" {
		return messages
	}
	out := make([]provider.Message, 0, start+1+len(messages)-cut)
	out = append(out, messages[:start]...)
	out = append(out, provider.Message{Role: "user", Content: "[Summary of earlier conversation]\n" + summary})
	out = append(out, messages[cut:]...)
	if r.Bus != nil {
		r.Bus.Publish(r.event(event.Summarized, r.Model, fmt.Sprintf("summarized %d messages", len(middle)), nil))
	}
	return out
}

func (r *Runtime) summarize(ctx context.Context, msgs []provider.Message) (string, error) {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "  [tool %s %s]\n", tc.Function.Name, tc.Function.Arguments)
		}
	}
	model := r.SummarizerModel
	if model == "" {
		model = r.Model
	}
	resp, err := r.LLM.Complete(ctx, llm.Request{
		Model: model,
		Messages: []provider.Message{
			{Role: "system", Content: "Compress the conversation into a concise factual summary. Preserve decisions, file paths, code changes, constraints and open questions. Omit pleasantries."},
			{Role: "user", Content: b.String()},
		},
		MaxTokens: 1024,
	})
	if err != nil {
		return "", err
	}
	return resp.Message.Content, nil
}

func addUsage(dst *provider.Usage, src provider.Usage) {
	dst.PromptTokens += src.PromptTokens
	dst.CompletionTokens += src.CompletionTokens
	dst.TotalTokens += src.TotalTokens
}

func estimateTokens(messages []provider.Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) + len(m.Name) + len(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	if total == 0 {
		return 0
	}
	return total/4 + 1
}

func truncateForEvent(s string) string {
	const max = 64 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
