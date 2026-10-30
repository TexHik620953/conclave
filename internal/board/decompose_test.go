package board

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tools"
	"github.com/texhik/conclave/internal/tracker"
)

func TestDecompositionInheritsParentContext(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tr, err := tracker.New(st.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tr.Create(context.Background(), tracker.CreateInput{
		Title: "Parent", Description: "Parent work", Status: tracker.StatusInProgress,
		Component: "api", Labels: []string{"backend"}, Requirements: []string{"keep API stable"},
		Constraints: []string{"no breaking changes"}, Verification: []string{"go test ./..."},
		OutOfScope: []string{"frontend"}, Acceptance: []string{"parent accepted"},
		WriteScope: []string{"internal/api"}, RunID: "run_1", BatchID: "run_1", CreatedBy: "worker",
	}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"reason": "too large", "handoff": "models already updated",
		"children": []map[string]any{{
			"title": "Child", "description": "Child work", "context": "child detail",
			"complexity": 2, "priority": 3, "write_scope": []string{"internal/api/handlers"},
			"acceptance": []string{"child accepted"},
		}},
	})
	tool := requestDecompositionTool{tr: tr}
	if _, err := tool.Run(context.Background(), tools.Env{TaskID: parent.ID, AgentID: "worker", SessionID: "ses_1", Cwd: dir}, args); err != nil {
		t.Fatal(err)
	}
	children, err := tr.List(context.Background(), tracker.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var child *tracker.Task
	for i := range children {
		if children[i].ParentID == parent.ID {
			child = &children[i]
		}
	}
	if child == nil {
		t.Fatal("child task not created")
	}
	if child.Component != "api" || child.BatchID != "run_1" || child.RunID != "run_1" {
		t.Fatalf("inherited fields = component %q batch %q run %q", child.Component, child.BatchID, child.RunID)
	}
	if len(child.WriteScope) != 1 || child.WriteScope[0] != "internal/api/handlers" {
		t.Fatalf("write scope = %v", child.WriteScope)
	}
	for _, want := range []string{"Parent task", "too large", "models already updated", "child detail", "keep API stable"} {
		if !strings.Contains(child.Context, want) {
			t.Fatalf("child context missing %q:\n%s", want, child.Context)
		}
	}
	updated, _ := tr.Get(context.Background(), parent.ID)
	if updated.Status != tracker.StatusNeedsPlanning {
		t.Fatalf("parent status = %s", updated.Status)
	}
}
