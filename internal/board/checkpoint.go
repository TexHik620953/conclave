package board

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/gitx"
)

// taskCheckpoint keeps the worktree and conversation of an unfinished task.
// Pausing a run cancels the agent but must not reset its progress.
type taskCheckpoint struct {
	TaskID    string           `json:"task_id"`
	RepoDir   string           `json:"repo_dir"`
	Branch    string           `json:"branch"`
	Dir       string           `json:"dir"`
	Base      string           `json:"base"`
	Messages  []domain.Message `json:"messages"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func checkpointPath(boardDir, taskID string) string {
	name := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(taskID)
	return filepath.Join(boardDir, name+".resume.json")
}

func loadTaskCheckpoint(boardDir, taskID string) (*taskCheckpoint, bool) {
	b, err := os.ReadFile(checkpointPath(boardDir, taskID))
	if err != nil {
		return nil, false
	}
	var cp taskCheckpoint
	if json.Unmarshal(b, &cp) != nil || cp.Dir == "" {
		return nil, false
	}
	if _, err := os.Stat(cp.Dir); err != nil {
		return nil, false
	}
	return &cp, true
}

func saveTaskCheckpoint(boardDir string, cp *taskCheckpoint) {
	if cp == nil {
		return
	}
	cp.UpdatedAt = time.Now()
	b, err := json.Marshal(cp)
	if err != nil {
		return
	}
	path := checkpointPath(boardDir, cp.TaskID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func removeTaskCheckpoint(boardDir, taskID string) {
	_ = os.Remove(checkpointPath(boardDir, taskID))
}

func findExistingWorktree(ctx context.Context, boardDir, taskID string) (string, string, bool) {
	matches, _ := filepath.Glob(filepath.Join(boardDir, taskID+"-a*"))
	sort.Slice(matches, func(i, j int) bool {
		si, ei := os.Stat(matches[i])
		sj, ej := os.Stat(matches[j])
		if ei != nil || ej != nil {
			return matches[i] > matches[j]
		}
		return si.ModTime().After(sj.ModTime())
	})
	for _, dir := range matches {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		repo := gitx.Repo{Dir: dir}
		if !repo.IsRepo(ctx) {
			continue
		}
		branch, err := repo.CurrentBranch(ctx)
		if err != nil || branch == "" || branch == "HEAD" {
			continue
		}
		return dir, branch, true
	}
	return "", "", false
}

func repairCheckpointMessages(msgs []domain.Message) []domain.Message {
	existing := map[string]bool{}
	for _, m := range msgs {
		if m.Role == domain.RoleTool && m.ToolCallID != "" {
			existing[m.ToolCallID] = true
		}
	}
	var missing []domain.Message
	for _, m := range msgs {
		if m.Role != domain.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && !existing[tc.ID] {
				existing[tc.ID] = true
				missing = append(missing, domain.Message{
					ID: domain.NewID("tool"), Role: domain.RoleTool, ToolCallID: tc.ID, Name: tc.Name,
					Content:   fmt.Sprintf("ERROR: tool %s was interrupted before returning a result. Check the worktree state and retry it if needed.", tc.Name),
					CreatedAt: time.Now(),
				})
			}
		}
	}
	return append(msgs, missing...)
}
