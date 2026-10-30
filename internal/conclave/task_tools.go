package conclave

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
	"github.com/texhik/conclave/internal/tracker"
)

type createTaskTool struct{ tr *tracker.Service }

func (t createTaskTool) Name() string { return "create_task" }
func (t createTaskTool) Spec() llm.Tool {
	return llm.Tool{Name: "create_task", Description: "Create a persistent project task in Ready. Use it for follow-up work outside the current scope.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"context":     map[string]any{"type": "string", "description": "Background, links, constraints and relevant files"},
				"complexity":  map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"priority":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"component":   map[string]any{"type": "string"},
				"labels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"epic":        map[string]any{"type": "string"},
				"depends_on":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"write_scope": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"acceptance":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"title", "description", "complexity", "priority"},
		}}
}
func (t createTaskTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	if t.tr == nil {
		return "", fmt.Errorf("task tracker is unavailable")
	}
	var in struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Context     string   `json:"context"`
		Complexity  int      `json:"complexity"`
		Priority    int      `json:"priority"`
		Component   string   `json:"component"`
		Labels      []string `json:"labels"`
		Epic        string   `json:"epic"`
		DependsOn   []string `json:"depends_on"`
		WriteScope  []string `json:"write_scope"`
		Acceptance  []string `json:"acceptance"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	task, err := t.tr.Create(ctx, tracker.CreateInput{
		Title: in.Title, Description: in.Description, Context: in.Context,
		Complexity: in.Complexity, Priority: in.Priority, Status: tracker.StatusReady,
		Component: in.Component, Labels: in.Labels, Epic: in.Epic,
		DependsOn: in.DependsOn, WriteScope: in.WriteScope, Acceptance: in.Acceptance,
		CreatedBy: actor(env), OriginSessionID: env.SessionID, Assignee: "",
	}, actor(env))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created %s: %s", task.ID, task.Title), nil
}

type listTasksTool struct{ tr *tracker.Service }

func (t listTasksTool) Name() string { return "list_tasks" }
func (t listTasksTool) Spec() llm.Tool {
	return llm.Tool{Name: "list_tasks", Description: "List persistent project tasks, optionally filtered by status or text.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"status": map[string]any{"type": "string"},
			"search": map[string]any{"type": "string"},
			"limit":  map[string]any{"type": "integer"},
		}}}
}
func (t listTasksTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	if t.tr == nil {
		return "", fmt.Errorf("task tracker is unavailable")
	}
	var in struct {
		Status string `json:"status"`
		Search string `json:"search"`
		Limit  int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &in)
	f := tracker.Filter{Search: in.Search, Limit: in.Limit}
	if in.Status != "" {
		f.Status = []tracker.TaskStatus{tracker.TaskStatus(in.Status)}
	}
	tasks, err := t.tr.List(ctx, f)
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "no tasks", nil
	}
	var b strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&b, "%s [%s] p%d c%d %s\n", task.ID, task.Status, task.Priority, task.Complexity, task.Title)
	}
	return b.String(), nil
}

type commentTaskTool struct{ tr *tracker.Service }

func (t commentTaskTool) Name() string { return "comment_task" }
func (t commentTaskTool) Spec() llm.Tool {
	return llm.Tool{Name: "comment_task", Description: "Add a comment to a project task.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"task_id": map[string]any{"type": "string"},
			"body":    map[string]any{"type": "string"},
		}, "required": []string{"task_id", "body"}}}
}
func (t commentTaskTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	if t.tr == nil {
		return "", fmt.Errorf("task tracker is unavailable")
	}
	var in struct {
		TaskID string `json:"task_id"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if _, err := t.tr.AddComment(ctx, in.TaskID, actor(env), in.Body); err != nil {
		return "", err
	}
	return "comment added to " + in.TaskID, nil
}

func TaskTools(tr *tracker.Service) []tools.Tool {
	return []tools.Tool{
		suggestTaskTool{tr: tr},
		listTasksTool{tr: tr},
		commentTaskTool{tr: tr},
	}
}

type suggestTaskTool struct{ tr *tracker.Service }

func (t suggestTaskTool) Name() string { return "suggest_task" }
func (t suggestTaskTool) Spec() llm.Tool {
	return llm.Tool{Name: "suggest_task", Description: "Record a follow-up task suggestion for master review. This does not create or dispatch a task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"context":     map[string]any{"type": "string", "description": "Why it matters, files and constraints"},
				"complexity":  map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"priority":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"acceptance":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"title", "description", "complexity", "priority"},
		}}
}
func (t suggestTaskTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	if t.tr == nil || env.TaskID == "" {
		return "", fmt.Errorf("task suggestion requires an active task")
	}
	var in struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Context     string   `json:"context"`
		Complexity  int      `json:"complexity"`
		Priority    int      `json:"priority"`
		Acceptance  []string `json:"acceptance"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SUGGESTION\nTitle: %s\nDescription: %s\nContext: %s\nComplexity: %d\nPriority: %d\n", in.Title, in.Description, in.Context, in.Complexity, in.Priority)
	for _, a := range in.Acceptance {
		fmt.Fprintf(&b, "Acceptance: %s\n", a)
	}
	if _, err := t.tr.AddComment(ctx, env.TaskID, env.AgentID, b.String()); err != nil {
		return "", err
	}
	return "Suggestion recorded for master review; no task was created.", nil
}

func actor(env tools.Env) string {
	if env.AgentID != "" {
		return env.AgentID
	}
	return "agent"
}

type listWorkersTool struct{ profile config.Profile }

func (t listWorkersTool) Name() string { return "list_workers" }
func (t listWorkersTool) Spec() llm.Tool {
	return llm.Tool{Name: "list_workers", Description: "List available worker profile IDs and their capabilities. Use these IDs for task assignment.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
}
func (t listWorkersTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var b strings.Builder
	for _, w := range t.profile.Workers {
		fmt.Fprintf(&b, "%s model=%s effort=%s complexity=%v instances=%d tools=%s\n", w.ID, w.Model, w.Effort, w.Complexity, w.Instances, strings.Join(w.Tools, ","))
	}
	if b.Len() == 0 {
		return "no worker profiles", nil
	}
	return b.String(), nil
}

type approveResultTool struct{ svc *Service }

func (t approveResultTool) Name() string { return "approve_result" }
func (t approveResultTool) Spec() llm.Tool {
	return llm.Tool{Name: "approve_result", Description: "Approve a reviewed task result and move the task to Done.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"task_id":  map[string]any{"type": "string", "description": "Board task ID"},
			"feedback": map[string]any{"type": "string"},
		}, "required": []string{"task_id"}}}
}
func (t approveResultTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in struct {
		TaskID   string `json:"task_id"`
		Feedback string `json:"feedback"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if err := t.svc.approveBoardTask(ctx, in.TaskID, actor(env), in.Feedback); err != nil {
		return "", err
	}
	return "Approved " + in.TaskID + ".", nil
}

type rejectResultTool struct{ svc *Service }

func (t rejectResultTool) Name() string { return "reject_result" }
func (t rejectResultTool) Spec() llm.Tool {
	return llm.Tool{Name: "reject_result", Description: "Reject a reviewed task result, leave concrete feedback and return the task to Ready for another attempt.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"task_id":  map[string]any{"type": "string", "description": "Board task ID"},
			"feedback": map[string]any{"type": "string", "description": "What must be fixed in the next attempt"},
		}, "required": []string{"task_id", "feedback"}}}
}
func (t rejectResultTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in struct {
		TaskID   string `json:"task_id"`
		Feedback string `json:"feedback"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if err := t.svc.rejectBoardTask(ctx, in.TaskID, actor(env), in.Feedback); err != nil {
		return "", err
	}
	return "Rejected " + in.TaskID + "; task returned to Ready.", nil
}
