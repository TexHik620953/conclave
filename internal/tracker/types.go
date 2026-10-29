package tracker

import (
	"crypto/sha1"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TaskStatus string

const (
	StatusBacklog       TaskStatus = "backlog"
	StatusReady         TaskStatus = "ready"
	StatusClaimed       TaskStatus = "claimed"
	StatusInProgress    TaskStatus = "in_progress"
	StatusNeedsPlanning TaskStatus = "needs_planning"
	StatusReview        TaskStatus = "review"
	StatusDone          TaskStatus = "done"
	StatusBlocked       TaskStatus = "blocked"
	StatusCancelled     TaskStatus = "cancelled"
)

type Task struct {
	ID               string     `json:"id"`
	ProjectID        string     `json:"project_id"`
	Number           int        `json:"number"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	Context          string     `json:"context"`
	Requirements     []string   `json:"requirements,omitempty"`
	Constraints      []string   `json:"constraints,omitempty"`
	Verification     []string   `json:"verification,omitempty"`
	OutOfScope       []string   `json:"out_of_scope,omitempty"`
	Complexity       int        `json:"complexity"`
	ComplexityReason string     `json:"complexity_reason,omitempty"`
	Priority         int        `json:"priority"`
	Status           TaskStatus `json:"status"`
	Component        string     `json:"component,omitempty"`
	Labels           []string   `json:"labels,omitempty"`
	Epic             string     `json:"epic,omitempty"`
	Kind             string     `json:"kind,omitempty"`
	ParentID         string     `json:"parent_id,omitempty"`
	DependsOn        []string   `json:"depends_on,omitempty"`
	WriteScope       []string   `json:"write_scope,omitempty"`
	Acceptance       []string   `json:"acceptance,omitempty"`
	CreatedBy        string     `json:"created_by"`
	OriginSessionID  string     `json:"origin_session_id,omitempty"`
	Assignee         string     `json:"assignee,omitempty"`
	RunID            string     `json:"run_id,omitempty"`
	BatchID          string     `json:"batch_id,omitempty"`
	Attempts         int        `json:"attempts"`
	Branch           string     `json:"branch,omitempty"`
	Commit           string     `json:"commit,omitempty"`
	ClaimOwner       string     `json:"claim_owner,omitempty"`
	ClaimUntil       *time.Time `json:"claim_until,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ReadyAt          *time.Time `json:"ready_at,omitempty"`
}

type Comment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type Event struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Type      string    `json:"type"`
	Actor     string    `json:"actor"`
	Data      string    `json:"data,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Project struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Root      string    `json:"root"`
	CreatedAt time.Time `json:"created_at"`
}

type Filter struct {
	Status     []TaskStatus
	Complexity []int
	Priority   []int
	Component  string
	Label      string
	Epic       string
	Assignee   string
	Search     string
	Limit      int
	Offset     int
}

type CreateInput struct {
	Title            string
	Description      string
	Context          string
	Requirements     []string
	Constraints      []string
	Verification     []string
	OutOfScope       []string
	ComplexityReason string
	Complexity       int
	Priority         int
	Status           TaskStatus
	Component        string
	Labels           []string
	Epic             string
	Kind             string
	ParentID         string
	DependsOn        []string
	WriteScope       []string
	Acceptance       []string
	CreatedBy        string
	OriginSessionID  string
	Assignee         string
}

type WorkerSlot struct {
	ID           string
	Complexities []int
	Labels       []string
}

// DetectProject derives a stable project identity from the git remote or the
// absolute repository path, and a Jira-like key from the directory name.
func DetectProject(cwd string) Project {
	root := cwd
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(out))
	}
	identity := root
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		if remote := normalizeRemote(strings.TrimSpace(string(out))); remote != "" {
			identity = remote
		}
	}
	sum := sha1.Sum([]byte(identity))
	id := "prj_" + hex.EncodeToString(sum[:8])
	base := filepath.Base(root)
	if base == "." || base == "/" || base == "" {
		base = "project"
	}
	return Project{ID: id, Key: projectKey(base), Name: base, Root: root}
}

func normalizeRemote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, "git@", "")
	s = strings.ReplaceAll(s, ":", "/")
	return strings.ToLower(s)
}

func projectKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if b.Len() < 6 {
				b.WriteRune(r)
			}
		}
	}
	if b.Len() == 0 {
		return "TASK"
	}
	return b.String()
}

type Attempt struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	AttemptNo  int       `json:"attempt_no"`
	AgentID    string    `json:"agent_id,omitempty"`
	Worker     string    `json:"worker,omitempty"`
	Model      string    `json:"model,omitempty"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	Commit     string    `json:"commit,omitempty"`
}

type LogEntry struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	AttemptID string    `json:"attempt_id"`
	Seq       int       `json:"seq"`
	Level     string    `json:"level"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
