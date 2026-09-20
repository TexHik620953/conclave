package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type memTodos struct{ items []Todo }

func (m *memTodos) Todos() []Todo           { return m.items }
func (m *memTodos) SetTodos(t []Todo) error { m.items = t; return nil }

type stubAsker struct {
	q Question
	a Answer
}

func (s *stubAsker) Ask(_ context.Context, q Question) (Answer, error) {
	s.q = q
	return s.a, nil
}

func TestTodoWriteAndRead(t *testing.T) {
	store := &memTodos{}
	env := &Env{Workspace: t.TempDir(), FSRead: true, Todos: store}
	reg := NewRegistry(env)

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]any{
			{"content": "design", "status": "completed", "priority": "high"},
			{"content": "implement", "status": "in_progress"},
		},
	})
	res, err := reg.Execute(context.Background(), "todo_write", args)
	if err != nil || res.IsError {
		t.Fatalf("todo_write: %v %+v", err, res)
	}
	if len(store.items) != 2 || store.items[0].ID == "" {
		t.Fatalf("todos not stored/identified: %+v", store.items)
	}

	read, err := reg.Execute(context.Background(), "todo_read", json.RawMessage("{}"))
	if err != nil || read.IsError {
		t.Fatalf("todo_read: %v %+v", err, read)
	}
	if want := "[x] design [high] (t1)\n[~] implement (t2)"; read.Content != want {
		t.Fatalf("rendered = %q, want %q", read.Content, want)
	}
}

func TestTodoMerge(t *testing.T) {
	store := &memTodos{items: []Todo{{ID: "t1", Content: "one", Status: TodoPending}}}
	env := &Env{Todos: store}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{
		"merge": true,
		"todos": []map[string]any{{"id": "t1", "content": "one", "status": "completed"}},
	})
	if _, err := reg.Execute(context.Background(), "todo_write", args); err != nil {
		t.Fatal(err)
	}
	if store.items[0].Status != TodoCompleted {
		t.Fatalf("merge did not update status: %+v", store.items[0])
	}
}

func TestAskUser(t *testing.T) {
	asker := &stubAsker{a: Answer{Selected: []string{"Postgres"}, Custom: "or SQLite"}}
	env := &Env{Asker: asker}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{
		"question":     "Which DB?",
		"multiple":     true,
		"allow_custom": true,
		"options":      []map[string]any{{"label": "Postgres"}, {"label": "MySQL"}},
	})
	res, err := reg.Execute(context.Background(), "ask_user", args)
	if err != nil || res.IsError {
		t.Fatalf("ask_user: %v %+v", err, res)
	}
	if asker.q.Question != "Which DB?" || !asker.q.Multiple || !asker.q.AllowCustom {
		t.Fatalf("question not passed through: %+v", asker.q)
	}
	if want := "selected: Postgres\ncustom: or SQLite"; res.Content != want {
		t.Fatalf("answer = %q, want %q", res.Content, want)
	}
}

func TestAskUserWithoutAsker(t *testing.T) {
	env := &Env{}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{"question": "hi"})
	res, err := reg.Execute(context.Background(), "ask_user", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error result without an asker")
	}
}
