package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
)

// CriticInput is what the critic reviews.
type CriticInput struct {
	Spec     string
	Playbook playbook.Playbook
	Summary  string
	Steps    []Step
}

// CriticVerdict is the critic's decision.
type CriticVerdict struct {
	Pass          bool   `json:"pass"`
	Feedback      string `json:"feedback"`
	SuggestedTier string `json:"suggested_tier,omitempty"`
}

// Critic reviews a playbook result against the spec.
type Critic interface {
	Review(ctx context.Context, in CriticInput) (CriticVerdict, error)
}

// LLMCritic reviews via an LLM using a `verdict` tool.
type LLMCritic struct {
	Client llmgw.Client
	Model  string
	// DefaultModel is used when Model is empty.
	DefaultModel string
	// Models resolves the critic model from live config.
	Models llmgw.ModelResolver
}

func (c *LLMCritic) model() string {
	if c.Models != nil {
		if m := c.Models.BrainModel(domain.BrainCritic); m != "" {
			return m
		}
	}
	if c.Model != "" {
		return c.Model
	}
	return c.DefaultModel
}

// Review implements Critic.
func (c *LLMCritic) Review(ctx context.Context, in CriticInput) (CriticVerdict, error) {
	if c.Client == nil {
		return CriticVerdict{}, fmt.Errorf("critic: no client configured")
	}
	model := c.model()
	resp, err := c.Client.Complete(ctx, llmgw.Request{
		Model:    model,
		System:   criticSystemPrompt(),
		Messages: []llmgw.Message{{Role: "user", Content: criticUserPrompt(in)}},
		Tools:    criticTools(),
	})
	if err != nil {
		return CriticVerdict{}, err
	}
	for _, tc := range resp.Message.ToolCalls {
		if tc.Name != "verdict" {
			continue
		}
		var v CriticVerdict
		if err := json.Unmarshal([]byte(tc.Arguments), &v); err != nil {
			return CriticVerdict{}, fmt.Errorf("critic: verdict arguments: %w", err)
		}
		return v, nil
	}
	return CriticVerdict{}, fmt.Errorf("critic: model did not return a verdict")
}

func criticSystemPrompt() string {
	return `You are a strict quality reviewer. Check the produced result against the
spec. Prefer objective issues (missing requirements, wrong assumptions,
unhandled cases). If the result is not acceptable, set pass=false and give
concrete, actionable feedback. If a stronger role tier is needed to fix it, set
suggested_tier (junior|middle|senior). Call the verdict tool.`
}

func criticUserPrompt(in CriticInput) string {
	var sb strings.Builder
	sb.WriteString("## Spec\n")
	sb.WriteString(in.Spec)
	sb.WriteString("\n\n## Result summary\n")
	sb.WriteString(in.Summary)
	if len(in.Steps) > 0 {
		sb.WriteString("\n\n## Steps\n")
		for i, s := range in.Steps {
			fmt.Fprintf(&sb, "%d. [%s] %s\n%s\n", i+1, s.Role, s.Task, s.Output)
		}
	}
	return sb.String()
}

func criticTools() []llmgw.ToolDef {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pass":           map[string]any{"type": "boolean"},
			"feedback":       map[string]any{"type": "string"},
			"suggested_tier": map[string]any{"type": "string", "enum": []string{"junior", "middle", "senior"}},
		},
		"required": []string{"pass"},
	}
	b, _ := json.Marshal(schema)
	return []llmgw.ToolDef{{Name: "verdict", Description: "Give the review verdict.", Parameters: b}}
}

// FakeCritic returns scripted verdicts, then passes.
type FakeCritic struct {
	Verdicts []CriticVerdict

	mu    sync.Mutex
	calls []CriticInput
}

// Review implements Critic.
func (f *FakeCritic) Review(_ context.Context, in CriticInput) (CriticVerdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if len(f.Verdicts) > 0 {
		v := f.Verdicts[0]
		f.Verdicts = f.Verdicts[1:]
		return v, nil
	}
	return CriticVerdict{Pass: true}, nil
}

// Calls returns the inputs observed so far.
func (f *FakeCritic) Calls() []CriticInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]CriticInput, len(f.calls))
	copy(out, f.calls)
	return out
}
