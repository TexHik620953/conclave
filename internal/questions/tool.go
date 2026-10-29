package questions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
)

type AskTool struct {
	Svc        *Service
	Autonomous bool
}

func (t AskTool) Name() string { return "ask_user" }
func (t AskTool) Spec() llm.Tool {
	return llm.Tool{
		Name:        "ask_user",
		Description: "Ask the user one or more clarifying questions. Supports single, multiple and custom text answers. In autonomous tasks the task becomes Blocked and this tool must be the last action.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":         map[string]any{"type": "string"},
				"category":      map[string]any{"type": "string", "enum": []string{"clarification", "decision", "permission"}},
				"agent_context": map[string]any{"type": "string", "description": "Short context: what the agent was doing and why it is blocked"},
				"questions": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":           map[string]any{"type": "string"},
							"text":         map[string]any{"type": "string"},
							"type":         map[string]any{"type": "string", "enum": []string{"single", "multiple", "text"}},
							"allow_custom": map[string]any{"type": "boolean"},
							"required":     map[string]any{"type": "boolean"},
							"options": map[string]any{"type": "array", "items": map[string]any{
								"type": "object", "properties": map[string]any{
									"id": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"},
									"description": map[string]any{"type": "string"}, "recommended": map[string]any{"type": "boolean"},
								}, "required": []string{"label"},
							}},
						},
						"required": []string{"text", "type"},
					},
				},
			},
			"required": []string{"questions"},
		},
	}
}
func (t AskTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	if t.Svc == nil {
		return "", fmt.Errorf("question service unavailable")
	}
	var in struct {
		Title        string     `json:"title"`
		Category     string     `json:"category"`
		AgentContext string     `json:"agent_context"`
		Questions    []Question `json:"questions"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	for i := range in.Questions {
		if in.Questions[i].ID == "" {
			in.Questions[i].ID = fmt.Sprintf("q%d", i+1)
		}
		if in.Questions[i].Type == "" {
			in.Questions[i].Type = "text"
		}
		for j := range in.Questions[i].Options {
			if in.Questions[i].Options[j].ID == "" {
				in.Questions[i].Options[j].ID = fmt.Sprintf("o%d", j+1)
			}
		}
	}
	set, err := t.Svc.Create(ctx, Set{
		SessionID: env.SessionID, TaskID: env.TaskID, AttemptID: env.ExtraString("attempt_id"),
		AgentID: env.AgentID, Title: in.Title, Category: in.Category,
		AgentContext: in.AgentContext, Questions: in.Questions,
	}, t.Autonomous)
	if err != nil {
		return "", err
	}
	if t.Autonomous {
		return fmt.Sprintf("Questions %s submitted. Task is Blocked by questions. Stop and wait for answers.", set.ID), nil
	}
	answers, err := t.Svc.Wait(ctx, set)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(answers.Answers)
	return string(b), nil
}
