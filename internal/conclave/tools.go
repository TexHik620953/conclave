package conclave

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
)

type submitPlanTool struct {
	svc *Service
}

func (t submitPlanTool) Name() string { return "submit_plan" }
func (t submitPlanTool) Spec() llm.Tool {
	return llm.Tool{
		Name: "submit_plan",
		Description: "Submit a pool of isolated worker tasks as a dependency DAG. " +
			"Call this only when the plan is concrete. Task IDs must be unique within the pool.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal":        map[string]any{"type": "string"},
				"constraints": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"acceptance":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":          map[string]any{"type": "string"},
							"title":       map[string]any{"type": "string"},
							"spec":        map[string]any{"type": "string"},
							"complexity":  map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
							"priority":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
							"acceptance":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"depends_on":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"write_scope": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"worker":      map[string]any{"type": "string", "description": "Worker profile ID"},
							"kind":        map[string]any{"type": "string", "enum": []string{"work", "research", "epic"}},
						},
						"required": []string{"id", "title", "spec", "worker"},
					},
				},
			},
			"required": []string{"goal", "tasks"},
		},
	}
}
func (t submitPlanTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var plan domain.Plan
	if err := json.Unmarshal(args, &plan); err != nil {
		return "", fmt.Errorf("invalid plan JSON: %w", err)
	}
	if err := t.svc.submitPlan(ctx, plan); err != nil {
		return "", err
	}
	return "Plan accepted and waiting for user approval.", nil
}

type finishRunTool struct {
	svc *Service
}

func (t finishRunTool) Name() string { return "finish_run" }
func (t finishRunTool) Spec() llm.Tool {
	return llm.Tool{
		Name:        "finish_run",
		Description: "Finish the Conclave run when the goal and acceptance criteria are satisfied.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "Final summary for the user"},
			},
			"required": []string{"summary"},
		},
	}
}
func (t finishRunTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if err := t.svc.canFinish(); err != nil {
		return "", err
	}
	t.svc.finish(in.Summary)
	return "Run marked complete.", nil
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "task"
	}
	return out
}
