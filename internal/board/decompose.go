package board

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/internal/gitx"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
	"github.com/texhik/conclave/internal/tracker"
)

type requestDecompositionTool struct{ tr *tracker.Service }

func (t requestDecompositionTool) Name() string { return "request_decomposition" }
func (t requestDecompositionTool) Spec() llm.Tool {
	return llm.Tool{
		Name:        "request_decomposition",
		Description: "Stop and request decomposition of the current task if it is larger than expected. Creates child tasks with inherited parent context and moves the parent to Needs Planning.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason":  map[string]any{"type": "string", "description": "Why the task needs to be split"},
				"handoff": map[string]any{"type": "string", "description": "Concise handoff: decisions made, work already done, files/modules touched and what must not be changed"},
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
		Handoff  string `json:"handoff"`
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

	baseCommit := ""
	repo := gitx.Repo{Dir: env.Cwd}
	if repo.IsRepo(ctx) {
		if snapshot, err := repo.Snapshot(ctx); err == nil {
			baseCommit = snapshot
		}
	}

	for _, c := range in.Children {
		if c.Complexity > 3 {
			c.Complexity = 3
		}
		childContext := buildChildContext(parent, c.Context, in.Reason, in.Handoff, env.Cwd, baseCommit)
		_, err := t.tr.Create(ctx, tracker.CreateInput{
			Title: c.Title, Description: c.Description, Context: childContext,
			Complexity: c.Complexity, Priority: c.Priority, Status: tracker.StatusReady,
			Component: parent.Component, Labels: append([]string(nil), parent.Labels...), Epic: parent.Epic,
			Requirements: append([]string(nil), parent.Requirements...),
			Constraints:  append([]string(nil), parent.Constraints...),
			Verification: append([]string(nil), parent.Verification...),
			OutOfScope:   append([]string(nil), parent.OutOfScope...),
			WriteScope:   inheritedWriteScope(parent.WriteScope, c.WriteScope),
			Acceptance:   c.Acceptance,
			CreatedBy:    env.AgentID, OriginSessionID: env.SessionID,
			Kind: "work", ParentID: parent.ID,
			RunID: parent.RunID, BatchID: parent.BatchID, BaseCommit: baseCommit,
		}, env.AgentID)
		if err != nil {
			return "", err
		}
	}

	_ = t.tr.SetTaskStatus(ctx, parent.ID, tracker.StatusNeedsPlanning, env.AgentID)
	comment := "Decomposition requested: " + strings.TrimSpace(in.Reason)
	if baseCommit != "" {
		comment += "\nChild base snapshot: " + baseCommit
	}
	_, _ = t.tr.AddComment(ctx, parent.ID, env.AgentID, comment)
	return "Parent moved to Needs Planning. Child tasks created in Ready with inherited context. Stop working on this task.", nil
}

func buildChildContext(parent *tracker.Task, childContext, reason, handoff, parentWorktree, baseCommit string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Parent task %s: %s\n", parent.ID, parent.Title)
	writeContextBlock(&b, "Parent description", parent.Description)
	writeContextList(&b, "Parent requirements", parent.Requirements)
	writeContextList(&b, "Parent constraints", parent.Constraints)
	writeContextList(&b, "Parent verification", parent.Verification)
	writeContextList(&b, "Parent out of scope", parent.OutOfScope)
	writeContextList(&b, "Parent acceptance", parent.Acceptance)
	writeContextBlock(&b, "Decomposition reason", reason)
	writeContextBlock(&b, "Handoff from parent", handoff)
	writeContextBlock(&b, "Child-specific context", childContext)
	if parentWorktree != "" {
		fmt.Fprintf(&b, "Parent worktree: %s\n", parentWorktree)
	}
	if baseCommit != "" {
		fmt.Fprintf(&b, "Child base commit (parent snapshot): %s\n", baseCommit)
	}
	b.WriteString("Do not repeat completed parent work. Inspect the inherited state and implement only this child task.")
	return truncateText(strings.TrimSpace(b.String()), 24000)
}

func writeContextBlock(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "\n%s:\n%s\n", label, truncateText(value, 6000))
}

func writeContextList(b *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", label)
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(b, "- %s\n", value)
		}
	}
}

func inheritedWriteScope(parent, child []string) []string {
	if len(parent) == 0 {
		return append([]string(nil), child...)
	}
	if len(child) == 0 {
		return append([]string(nil), parent...)
	}
	var out []string
	for _, c := range child {
		cn := normalizeScope(c)
		for _, p := range parent {
			pn := normalizeScope(p)
			switch {
			case cn == pn:
				out = append(out, c)
			case strings.HasPrefix(cn, pn+"/"):
				out = append(out, c)
			case strings.HasPrefix(pn, cn+"/"):
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return append([]string(nil), child...)
	}
	return out
}

func normalizeScope(scope string) string {
	scope = strings.TrimSpace(strings.ReplaceAll(scope, "\\", "/"))
	scope = strings.TrimSuffix(scope, "/")
	scope = strings.TrimSuffix(scope, "/**")
	return scope
}

func truncateText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[truncated]"
}
