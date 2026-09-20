package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TodoStatus is the lifecycle state of a todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

// Todo is a single task item maintained by the manager role.
type Todo struct {
	ID       string     `json:"id"`
	Content  string     `json:"content"`
	Status   TodoStatus `json:"status"`
	Priority string     `json:"priority,omitempty"`
}

// TodoStore is the shared todo list for a run.
type TodoStore interface {
	Todos() []Todo
	SetTodos([]Todo) error
}

// RenderTodos formats a todo list as markdown checkboxes.
func RenderTodos(todos []Todo) string {
	if len(todos) == 0 {
		return "(no todos)"
	}
	var b strings.Builder
	for _, t := range todos {
		fmt.Fprintf(&b, "%s %s", todoGlyph(t.Status), t.Content)
		if t.Priority != "" {
			fmt.Fprintf(&b, " [%s]", t.Priority)
		}
		if t.ID != "" {
			fmt.Fprintf(&b, " (%s)", t.ID)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func todoGlyph(s TodoStatus) string {
	switch s {
	case TodoInProgress:
		return "[~]"
	case TodoCompleted:
		return "[x]"
	case TodoCancelled:
		return "[-]"
	default:
		return "[ ]"
	}
}

type todoWrite struct{ env *Env }

func (t *todoWrite) Name() string { return "todo_write" }
func (t *todoWrite) Description() string {
	return "Create or update the run's todo list. Replaces the list unless merge is true. Use this as the manager to track multi-step work."
}
func (t *todoWrite) Schema() json.RawMessage {
	return schema(map[string]any{
		"todos": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":       map[string]any{"type": "string"},
					"content":  map[string]any{"type": "string"},
					"status":   map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}},
					"priority": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
				},
				"required": []string{"content", "status"},
			},
		},
		"merge": map[string]any{"type": "boolean", "description": "Merge by id/content instead of replacing the whole list."},
	}, "todos")
}

func (t *todoWrite) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if t.env.Todos == nil {
		return Result{Content: "todo list is not available in this context", IsError: true}, nil
	}
	var in struct {
		Todos []Todo `json:"todos"`
		Merge bool   `json:"merge"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	for i := range in.Todos {
		if in.Todos[i].Status == "" {
			in.Todos[i].Status = TodoPending
		}
		if in.Todos[i].ID == "" {
			in.Todos[i].ID = fmt.Sprintf("t%d", i+1)
		}
	}
	todos := in.Todos
	if in.Merge {
		todos = mergeTodos(t.env.Todos.Todos(), in.Todos)
	}
	if err := t.env.Todos.SetTodos(todos); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: "todos updated:\n" + RenderTodos(todos)}, nil
}

type todoRead struct{ env *Env }

func (t *todoRead) Name() string { return "todo_read" }
func (t *todoRead) Description() string {
	return "Read the current todo list."
}
func (t *todoRead) Schema() json.RawMessage {
	return schema(map[string]any{})
}

func (t *todoRead) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if t.env.Todos == nil {
		return Result{Content: "todo list is not available in this context", IsError: true}, nil
	}
	return Result{Content: RenderTodos(t.env.Todos.Todos())}, nil
}

func mergeTodos(existing, incoming []Todo) []Todo {
	index := map[string]int{}
	for i, t := range existing {
		if t.ID != "" {
			index[t.ID] = i
		}
	}
	out := append([]Todo{}, existing...)
	for _, t := range incoming {
		pos, ok := index[t.ID]
		if !ok {
			out = append(out, t)
			index[t.ID] = len(out) - 1
			continue
		}
		if t.Content == "" {
			t.Content = out[pos].Content
		}
		out[pos] = t
	}
	return out
}
