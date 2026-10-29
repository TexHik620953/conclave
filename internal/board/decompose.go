package board

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
	"github.com/texhik/conclave/internal/tracker"
)

type requestDecompositionTool struct{ tr *tracker.Service }

func (t requestDecompositionTool) Name() string { return "request_decomposition" }
func (t requestDecompositionTool) Spec() llm.Tool {
	return llm.Tool{
		Name:        "request_decomposition",
		Description: "Stop and request decomposition of the current task if it is larger than expected. Creates child tasks and moves the parent to Needs Planning.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{"type": "string", "description": "Why the task needs to be split"},
				"children": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":       map[string]any{"type": "string"},
							"description": map[string]any{"type": "string"},
							"context":     map[string]any{"type": "string"},
							"complexity":  map[string]any{"type": "integer", "minimum": 1, "maximum": 3},
							"priority":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
							"write_scope": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"acceptance":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
						"required": []string{"title", "description", "complexity", "priority"},
					},
				},
			},
			"required": []string{"reason", "children"},
		},
	}
}
func (t requestDecompositionTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	if env.TaskID == "" {
		return "", fmt.Errorf("no task bound to this agent")
	}
	var in struct {
		Reason   string `json:"reason"`
		Children []struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Context     string   `json:"context"`
			Complexity  int      `json:"complexity"`
			Priority    int      `json:"priority"`
			WriteScope  []string `json:"write_scope"`
			Acceptance  []string `json:"acceptance"`
		} `json:"children"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if len(in.Children) == 0 || len(in.Children) > 7 {
		return "", fmt.Errorf("decomposition must contain 1..7 children")
	}
	parent, err := t.tr.Get(ctx, env.TaskID)
	if err != nil {
		return "", err
	}
	if len(parent.ParentID) > 0 {
		return "", fmt.Errorf("nested decomposition is not allowed")
	}
	for _, c := range in.Children {
		if c.Complexity > 3 {
			c.Complexity = 3
		}
		_, err := t.tr.Create(ctx, tracker.CreateInput{
			Title: c.Title, Description: c.Description, Context: c.Context,
			Complexity: c.Complexity, Priority: c.Priority, Status: tracker.StatusReady,
			WriteScope: c.WriteScope, Acceptance: c.Acceptance,
			CreatedBy: env.AgentID, OriginSessionID: env.SessionID,
			Kind: "work", ParentID: parent.ID,
		}, env.AgentID)
		if err != nil {
			return "", err
		}
	}
	_ = t.tr.SetTaskStatus(ctx, parent.ID, tracker.StatusNeedsPlanning, env.AgentID)
	_, _ = t.tr.AddComment(ctx, parent.ID, env.AgentID, "Decomposition requested: "+strings.TrimSpace(in.Reason))
	return "Parent moved to Needs Planning. Child tasks created in Ready. Stop working on this task.", nil
}
