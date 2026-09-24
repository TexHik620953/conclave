package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/stream"
	"github.com/texhik/conclave/conclave-core/internal/toolgw"
)

// LLMRoleRunner executes a role via an LLM, optionally calling tools through an
// Executor until the model produces a final answer. It streams assistant tokens
// to the sink attached to the context, if any.
type LLMRoleRunner struct {
	Client        llmgw.Client
	Tools         toolgw.Executor
	ToolDefs      []llmgw.ToolDef
	MaxIterations int
	// DefaultModel is used when a role does not pin a model.
	DefaultModel string
}

// Run implements RoleRunner.
func (r *LLMRoleRunner) Run(ctx context.Context, in RoleInput) (string, error) {
	if r.Client == nil {
		return "", fmt.Errorf("role runner: client is required")
	}
	maxIter := r.MaxIterations
	if maxIter <= 0 {
		maxIter = 8
	}
	model := in.Role.Model
	if model == "" {
		model = r.DefaultModel
	}

	messageID := uuid.NewString()
	var streamed strings.Builder
	finalized := false
	finalize := func(content string) {
		if finalized {
			return
		}
		finalized = true
		stream.Emit(ctx, stream.Delta{
			MessageID: messageID, Role: in.Role.Tier, Model: model, Text: content, Final: true,
		})
	}
	emitDelta := func(d llmgw.Delta) {
		if d.Content != "" {
			streamed.WriteString(d.Content)
		}
		stream.Emit(ctx, stream.Delta{
			MessageID: messageID, Role: in.Role.Tier, Model: model,
			Text: d.Content, Reasoning: d.Reasoning,
		})
	}

	defs := r.ToolDefs
	if provider, ok := r.Tools.(interface {
		ToolDefs(context.Context) []llmgw.ToolDef
	}); ok {
		if dynamic := provider.ToolDefs(ctx); len(dynamic) > 0 {
			defs = dynamic
		}
	}

	messages := []llmgw.Message{{Role: "user", Content: in.Task}}
	var last string
	for i := 0; i < maxIter; i++ {
		// Inject any user messages sent while this role is running.
		for _, msg := range stream.DrainInbox(ctx) {
			messages = append(messages, llmgw.Message{Role: "user", Content: msg})
		}
		resp, err := llmgw.Stream(ctx, r.Client, llmgw.Request{
			Model:    model,
			System:   in.System,
			Messages: messages,
			Tools:    defs,
		}, emitDelta)
		if err != nil {
			finalize(streamed.String())
			return "", err
		}
		assistant := resp.Message
		if assistant.Role == "" {
			assistant.Role = "assistant"
		}
		messages = append(messages, assistant)
		if assistant.Content != "" {
			last = assistant.Content
		}
		if len(assistant.ToolCalls) == 0 {
			finalize(last)
			return last, nil
		}
		for _, tc := range assistant.ToolCalls {
			result := toolgw.Result{Content: "no tool executor configured", IsError: true}
			if r.Tools != nil {
				res, err := r.Tools.Execute(ctx, toolgw.Call{
					ID: tc.ID, Name: tc.Name, Args: json.RawMessage(tc.Arguments),
				})
				if err != nil {
					finalize(streamed.String())
					return "", err
				}
				result = res
			}
			messages = append(messages, llmgw.Message{
				Role: "tool", ToolCallID: tc.ID, Name: tc.Name, Content: result.Content,
			})
		}
	}
	content := last
	if content == "" {
		content = streamed.String()
	}
	finalize(content)
	if content != "" {
		return content, nil
	}
	return "", fmt.Errorf("role runner: exceeded %d iterations without a final answer", maxIter)
}
