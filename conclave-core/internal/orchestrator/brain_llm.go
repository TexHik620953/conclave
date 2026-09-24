package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
)

// LLMBrain decides the next supervisor action by calling an LLM with delegate
// and finish tools.
type LLMBrain struct {
	Client    llmgw.Client
	Model     string
	MaxTokens int
	// DefaultModel is used when Model is empty.
	DefaultModel string
	// Models resolves the supervisor model from live config.
	Models llmgw.ModelResolver
}

func (b *LLMBrain) model() string {
	if b.Models != nil {
		if m := b.Models.BrainModel(domain.BrainSupervisor); m != "" {
			return m
		}
	}
	if b.Model != "" {
		return b.Model
	}
	return b.DefaultModel
}

// Decide implements Brain.
func (b *LLMBrain) Decide(ctx context.Context, in DecisionInput) (Decision, error) {
	if b.Client == nil {
		return Decision{}, fmt.Errorf("llm brain: client is required")
	}
	model := b.model()
	req := llmgw.Request{
		Model:     model,
		System:    b.systemPrompt(in),
		Messages:  []llmgw.Message{{Role: "user", Content: userPrompt(in)}},
		Tools:     supervisorTools(),
		MaxTokens: b.MaxTokens,
	}
	resp, err := b.Client.Complete(ctx, req)
	if err != nil {
		return Decision{}, err
	}
	return parseDecision(resp.Message)
}

func (b *LLMBrain) systemPrompt(in DecisionInput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are the supervisor of the %q playbook.\n", in.Playbook.Title)
	if in.Playbook.Guidelines != "" {
		sb.WriteString(strings.TrimSpace(in.Playbook.Guidelines))
		sb.WriteString("\n")
	}
	sb.WriteString("\nAvailable roles (pass the exact tier to `delegate`):\n")
	for _, r := range in.Playbook.Roles {
		fmt.Fprintf(&sb, "- %s (model %s)\n", r.Tier, r.Model)
	}
	sb.WriteString("\nDecide the single next action. Call `delegate` to run a role, or `finish` when the work is complete.\n")
	return sb.String()
}

func userPrompt(in DecisionInput) string {
	var sb strings.Builder
	if in.Spec != "" {
		sb.WriteString("## Spec\n")
		sb.WriteString(in.Spec)
		sb.WriteString("\n")
	}
	if len(in.History) > 0 {
		sb.WriteString("\n## Work so far\n")
		for i, st := range in.History {
			fmt.Fprintf(&sb, "%d. [%s] %s\n%s\n", i+1, st.Role, st.Task, st.Output)
		}
	} else {
		sb.WriteString("\nNo work has been done yet. Start with the first step.\n")
	}
	return sb.String()
}

func supervisorTools() []llmgw.ToolDef {
	delegate := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"role": map[string]any{"type": "string", "description": "Role tier to run."},
			"task": map[string]any{"type": "string", "description": "Self-contained task for the role."},
		},
		"required": []string{"role", "task"},
	}
	finish := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "description": "Final summary of the result."},
		},
		"required": []string{"summary"},
	}
	db, _ := json.Marshal(delegate)
	fb, _ := json.Marshal(finish)
	return []llmgw.ToolDef{
		{Name: "delegate", Description: "Run a role on a task and get its output.", Parameters: db},
		{Name: "finish", Description: "Finish the playbook with a final summary.", Parameters: fb},
	}
}

func parseDecision(msg llmgw.Message) (Decision, error) {
	for _, tc := range msg.ToolCalls {
		switch tc.Name {
		case "delegate":
			var args struct {
				Role string `json:"role"`
				Task string `json:"task"`
			}
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				return Decision{}, fmt.Errorf("delegate arguments: %w", err)
			}
			if args.Role == "" || strings.TrimSpace(args.Task) == "" {
				return Decision{}, fmt.Errorf("delegate requires role and task")
			}
			return Decision{Delegate: &Delegation{Role: args.Role, Task: args.Task}}, nil
		case "finish":
			var args struct {
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				return Decision{}, fmt.Errorf("finish arguments: %w", err)
			}
			return Decision{Finish: args.Summary}, nil
		}
	}
	// No tool call: treat a non-empty answer as a finish summary.
	if strings.TrimSpace(msg.Content) != "" {
		return Decision{Finish: msg.Content}, nil
	}
	return Decision{}, fmt.Errorf("supervisor: model returned no action")
}
