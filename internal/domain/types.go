package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewID returns a short, sortable-enough identifier with a human prefix.
func NewID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s_%s%s", prefix, time.Now().UTC().Format("20060102150405"), hex.EncodeToString(b[:]))
}

type Mode string

const (
	ModeBuild    Mode = "build"
	ModePlan     Mode = "plan"
	ModeConclave Mode = "conclave"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Message struct {
	ID         string     `json:"id"`
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	Requests         int `json:"requests"`
}

func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.CachedTokens += other.CachedTokens
	u.Requests += other.Requests
}

func (u Usage) TotalTokens() int {
	return u.PromptTokens + u.CompletionTokens
}

type UsageRecord struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	AgentID         string `json:"agent_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CachedTokens    int    `json:"cached_tokens,omitempty"`
	ReasoningTokens int    `json:"reasoning_tokens,omitempty"`
	Requests        int    `json:"requests"`
	LastContext     int    `json:"last_context"`
	ContextWindow   int    `json:"context_window"`
}

func (r *UsageRecord) Add(other UsageRecord) {
	r.InputTokens += other.InputTokens
	r.OutputTokens += other.OutputTokens
	r.CachedTokens += other.CachedTokens
	r.ReasoningTokens += other.ReasoningTokens
	r.Requests += other.Requests
	if other.LastContext > 0 {
		r.LastContext = other.LastContext
	}
	if other.ContextWindow > 0 {
		r.ContextWindow = other.ContextWindow
	}
}

type UsageLedger struct {
	ByModel map[string]UsageRecord `json:"by_model,omitempty"`
	ByAgent map[string]UsageRecord `json:"by_agent,omitempty"`
}

func (l *UsageLedger) Add(r UsageRecord) {
	if l.ByModel == nil {
		l.ByModel = map[string]UsageRecord{}
	}
	if l.ByAgent == nil {
		l.ByAgent = map[string]UsageRecord{}
	}
	mk := r.Provider + "/" + r.Model
	m := l.ByModel[mk]
	if m.Provider == "" {
		m.Provider, m.Model = r.Provider, r.Model
	}
	m.Add(r)
	l.ByModel[mk] = m
	if r.AgentID != "" {
		a := l.ByAgent[r.AgentID]
		if a.AgentID == "" {
			a.AgentID = r.AgentID
		}
		a.Provider, a.Model = r.Provider, r.Model
		a.Add(r)
		l.ByAgent[r.AgentID] = a
	}
}

func (l UsageLedger) Models() []UsageRecord {
	out := make([]UsageRecord, 0, len(l.ByModel))
	for _, r := range l.ByModel {
		out = append(out, r)
	}
	return out
}

func (l UsageLedger) Agents() []UsageRecord {
	out := make([]UsageRecord, 0, len(l.ByAgent))
	for _, r := range l.ByAgent {
		out = append(out, r)
	}
	return out
}

type Session struct {
	ID               string      `json:"id"`
	Title            string      `json:"title"`
	Cwd              string      `json:"cwd"`
	Mode             Mode        `json:"mode"`
	Model            string      `json:"model"`
	Effort           string      `json:"effort"`
	PlanOnly         bool        `json:"plan_only"`
	Messages         []Message   `json:"messages"`
	ConclaveMessages []Message   `json:"conclave_messages,omitempty"`
	Usage            Usage       `json:"usage"`
	Ledger           UsageLedger `json:"ledger,omitempty"`
	ContextTokens    int         `json:"context_tokens"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

type Event struct {
	Seq         int64     `json:"seq"`
	Time        time.Time `json:"time"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	TaskID      string    `json:"task_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	Type        string    `json:"type"`
	Text        string    `json:"text,omitempty"`
	Payload     any       `json:"payload,omitempty"`
}

type RunPhase string

const (
	RunPlanning  RunPhase = "planning"
	RunApproval  RunPhase = "awaiting_approval"
	RunRunning   RunPhase = "running"
	RunReviewing RunPhase = "reviewing"
	RunPaused    RunPhase = "paused"
	RunCompleted RunPhase = "completed"
	RunFailed    RunPhase = "failed"
	RunCancelled RunPhase = "cancelled"
)

type TaskStatus string

const (
	TaskPending  TaskStatus = "pending"
	TaskBlocked  TaskStatus = "blocked"
	TaskQueued   TaskStatus = "queued"
	TaskRunning  TaskStatus = "running"
	TaskReview   TaskStatus = "review"
	TaskDone     TaskStatus = "done"
	TaskFailed   TaskStatus = "failed"
	TaskCanceled TaskStatus = "cancelled"
)

type Task struct {
	ID          string     `json:"id"`
	BoardID     string     `json:"board_id,omitempty"`
	Title       string     `json:"title"`
	Spec        string     `json:"spec"`
	Complexity  int        `json:"complexity,omitempty"`
	Priority    int        `json:"priority,omitempty"`
	Acceptance  []string   `json:"acceptance,omitempty"`
	DependsOn   []string   `json:"depends_on,omitempty"`
	WriteScope  []string   `json:"write_scope,omitempty"`
	WorkerID    string     `json:"worker"`
	Kind        string     `json:"kind,omitempty"`
	Model       string     `json:"model,omitempty"`
	Status      TaskStatus `json:"status"`
	Attempts    int        `json:"attempts"`
	Branch      string     `json:"branch,omitempty"`
	Worktree    string     `json:"worktree,omitempty"`
	Commit      string     `json:"commit,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Diff        string     `json:"diff,omitempty"`
	Error       string     `json:"error,omitempty"`
	Usage       Usage      `json:"usage"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	LastAgentID string     `json:"last_agent_id,omitempty"`
}

type Plan struct {
	Goal        string   `json:"goal"`
	Constraints []string `json:"constraints,omitempty"`
	Acceptance  []string `json:"acceptance,omitempty"`
	Tasks       []Task   `json:"tasks"`
}

type Run struct {
	ID                string      `json:"id"`
	SessionID         string      `json:"session_id"`
	Phase             RunPhase    `json:"phase"`
	Goal              string      `json:"goal,omitempty"`
	Plan              *Plan       `json:"plan,omitempty"`
	Tasks             []Task      `json:"tasks,omitempty"`
	Iteration         int         `json:"iteration"`
	MaxIterations     int         `json:"max_iterations"`
	BaseRef           string      `json:"base_ref,omitempty"`
	IntegrationBranch string      `json:"integration_branch,omitempty"`
	IntegrationDir    string      `json:"integration_worktree,omitempty"`
	RunDir            string      `json:"run_dir,omitempty"`
	Error             string      `json:"error,omitempty"`
	Usage             Usage       `json:"usage"`
	Ledger            UsageLedger `json:"ledger,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}
