package board

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/domain"
)

func TestTaskCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	worktree := filepath.Join(dir, "task-a1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	cp := &taskCheckpoint{TaskID: "TASK-1", RepoDir: dir, Branch: "b", Dir: worktree, Base: "abc", Messages: []domain.Message{{ID: "m1", Role: domain.RoleUser, Content: "work"}}}
	saveTaskCheckpoint(dir, cp)
	got, ok := loadTaskCheckpoint(dir, "TASK-1")
	if !ok || got.Branch != "b" || len(got.Messages) != 1 {
		t.Fatalf("checkpoint not restored: %#v ok=%v", got, ok)
	}
	removeTaskCheckpoint(dir, "TASK-1")
	if _, ok := loadTaskCheckpoint(dir, "TASK-1"); ok {
		t.Fatal("checkpoint still exists")
	}
}

func TestRepairCheckpointDanglingToolCall(t *testing.T) {
	msgs := []domain.Message{{
		ID: "a1", Role: domain.RoleAssistant, CreatedAt: time.Now(),
		ToolCalls: []domain.ToolCall{{ID: "call1", Name: "shell"}},
	}}
	repaired := repairCheckpointMessages(msgs)
	if len(repaired) != 2 || repaired[1].Role != domain.RoleTool || repaired[1].ToolCallID != "call1" {
		t.Fatalf("unexpected repaired messages: %#v", repaired)
	}
}
