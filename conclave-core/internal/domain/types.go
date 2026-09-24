// Package domain holds the core entities, identifiers and state machines of
// conclave-core. It has no dependencies on transport or persistence.
package domain

import (
	"encoding/json"
	"time"
)

// SessionStatus is the lifecycle state of a session.
type SessionStatus string

const (
	SessionActive    SessionStatus = "active"
	SessionPaused    SessionStatus = "paused"
	SessionDone      SessionStatus = "done"
	SessionFailed    SessionStatus = "failed"
	SessionCancelled SessionStatus = "cancelled"
)

// NodeState is the state of a plan node / playbook run.
type NodeState string

const (
	NodePending           NodeState = "pending"
	NodeReady             NodeState = "ready"
	NodeRunning           NodeState = "running"
	NodeWaitingDependency NodeState = "waiting_dependency"
	NodeWaitingUser       NodeState = "waiting_user"
	NodeReview            NodeState = "review"
	NodeDone              NodeState = "done"
	NodeFailed            NodeState = "failed"
	NodeCancelled         NodeState = "cancelled"
)

// TaskState is the state of a task within a playbook.
type TaskState string

const (
	TaskPending     TaskState = "pending"
	TaskRunning     TaskState = "running"
	TaskWaitingUser TaskState = "waiting_user"
	TaskReview      TaskState = "review"
	TaskDone        TaskState = "done"
	TaskFailed      TaskState = "failed"
	TaskCancelled   TaskState = "cancelled"
)

// PlanStatus is the state of a plan version.
type PlanStatus string

const (
	PlanDraft   PlanStatus = "draft"
	PlanActive  PlanStatus = "active"
	PlanDone    PlanStatus = "done"
	PlanFailed  PlanStatus = "failed"
	PlanRevised PlanStatus = "revised"
)

// EdgeKind classifies a dependency between plan nodes.
type EdgeKind string

const (
	EdgeDependency EdgeKind = "dependency" // wait for completion
	EdgeData       EdgeKind = "data"       // wait for an artifact
	EdgeReview     EdgeKind = "review"     // human approval, non-blocking
)

// Question kinds.
const (
	QuestionKindGate       = "gate"
	QuestionKindInterview  = "interview"
	QuestionKindController = "controller"
)

// QuestionState is the lifecycle of a question.
type QuestionState string

const (
	QuestionOpen      QuestionState = "open"
	QuestionAnswered  QuestionState = "answered"
	QuestionCancelled QuestionState = "cancelled"
)

// AutoPolicy controls automatic answering of questions.
type AutoPolicy string

const (
	// AutoInherit uses the session-level setting.
	AutoInherit AutoPolicy = "inherit"
	// AutoAlways answers with the first (recommended) option.
	AutoAlways AutoPolicy = "always"
	// AutoNever always waits for the user.
	AutoNever AutoPolicy = "never"
)

// Tenant is an isolated customer account.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// User is a person within a tenant.
type User struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// Device is a registered machine with a hashed device token.
type Device struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// SessionAgent binds a session to the device whose agent executes its tools.
type SessionAgent struct {
	SessionID string    `json:"session_id"`
	DeviceID  string    `json:"device_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserToken is a user-scoped API token (for the client/data WebSocket).
type UserToken struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	UserID    string     `json:"user_id"`
	TokenHash string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Project is a working directory (repository) on the user's machine.
type Project struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	RepoPath  string    `json:"repo_path"`
	CreatedAt time.Time `json:"created_at"`
}

// Session is a single user task; it aggregates all related state.
type Session struct {
	ID         string        `json:"id"`
	TenantID   string        `json:"tenant_id"`
	ProjectID  string        `json:"project_id"`
	Title      string        `json:"title"`
	Status     SessionStatus `json:"status"`
	AutoAccept bool          `json:"auto_accept"`
	BudgetUSD  float64       `json:"budget_usd"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

// Spec is a versioned requirements document produced by the interviewer.
type Spec struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Version   int       `json:"version"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// Plan is a versioned graph of playbook runs for a session.
type Plan struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Version   int        `json:"version"`
	Status    PlanStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
}

// Node kinds.
const (
	NodeKindPlaybook = "playbook"
	NodeKindGate     = "gate"
)

// PlanNode is one playbook instance (or human gate) in a plan. ID is the
// internal unique id; Key is the user-facing id within the plan (used by edges).
type PlanNode struct {
	ID              string    `json:"id"`
	Key             string    `json:"key"`
	PlanID          string    `json:"plan_id"`
	Kind            string    `json:"kind"`
	PlaybookID      string    `json:"playbook_id,omitempty"`
	RoleID          string    `json:"role_id,omitempty"`
	Grade           string    `json:"grade,omitempty"`
	PlaybookVersion int       `json:"playbook_version,omitempty"`
	Title           string    `json:"title"`
	Prompt          string    `json:"prompt,omitempty"`
	Output          string    `json:"output,omitempty"`
	State           NodeState `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PlanEdge connects two plan nodes.
type PlanEdge struct {
	PlanID string   `json:"plan_id"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Kind   EdgeKind `json:"kind"`
}

// PlaybookRun is a running instance of a playbook template (a thread).
type PlaybookRun struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	PlanNodeID      string    `json:"plan_node_id"`
	PlaybookID      string    `json:"playbook_id"`
	PlaybookVersion int       `json:"playbook_version"`
	State           NodeState `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Task is a unit of work inside a playbook, assigned to a role tier.
type Task struct {
	ID            string    `json:"id"`
	PlaybookRunID string    `json:"playbook_run_id"`
	Title         string    `json:"title"`
	Tier          string    `json:"tier"`
	State         TaskState `json:"state"`
	Attempts      int       `json:"attempts"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Attempt is a single execution attempt of a task.
type Attempt struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	N          int       `json:"n"`
	State      TaskState `json:"state"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Artifact is a named result of a playbook run.
type Artifact struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	PlaybookRunID string    `json:"playbook_run_id,omitempty"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Ref           string    `json:"ref"`
	CreatedAt     time.Time `json:"created_at"`
}

// Option is one choice in a question.
type Option struct {
	Label       string `json:"label"`
	Recommended bool   `json:"recommended"`
}

// Question is a request for user input. Ref points at the entity that opened
// it: a plan node id for gates, an interview id for interviews.
type Question struct {
	ID         string        `json:"id"`
	SessionID  string        `json:"session_id"`
	Kind       string        `json:"kind"`
	Ref        string        `json:"ref,omitempty"`
	Text       string        `json:"text"`
	Options    []Option      `json:"options"`
	AutoPolicy AutoPolicy    `json:"auto_policy"`
	State      QuestionState `json:"state"`
	CreatedAt  time.Time     `json:"created_at"`
}

// Answer is a response to a question.
type Answer struct {
	ID         string    `json:"id"`
	QuestionID string    `json:"question_id"`
	Selected   []string  `json:"selected,omitempty"`
	Custom     string    `json:"custom,omitempty"`
	Auto       bool      `json:"auto"`
	CreatedAt  time.Time `json:"created_at"`
}

// InterviewState is the lifecycle of a requirements interview.
type InterviewState string

const (
	InterviewRunning InterviewState = "running"
	InterviewWaiting InterviewState = "waiting"
	InterviewDone    InterviewState = "done"
	InterviewFailed  InterviewState = "failed"
)

// InterviewMessage is one turn of an interview transcript.
type InterviewMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Interview captures the clarifying dialogue that produces a Spec.
type Interview struct {
	ID                string             `json:"id"`
	SessionID         string             `json:"session_id"`
	Idea              string             `json:"idea"`
	State             InterviewState     `json:"state"`
	PendingQuestionID string             `json:"pending_question_id,omitempty"`
	Transcript        []InterviewMessage `json:"transcript,omitempty"`
	SpecID            string             `json:"spec_id,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// JobState is the lifecycle of a background job.
type JobState string

const (
	JobQueued    JobState = "queued"
	JobLeased    JobState = "leased"
	JobDone      JobState = "done"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// Job kinds.
const (
	JobRunPlan   = "run_plan"
	JobInterview = "interview"
	JobPlan      = "plan"
	JobSession   = "session"
)

// Job is a durable unit of work claimed by a worker with a lease.
type Job struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	SessionID       string          `json:"session_id"`
	PlanID          string          `json:"plan_id"`
	State           JobState        `json:"state"`
	Attempts        int             `json:"attempts"`
	MaxAttempts     int             `json:"max_attempts"`
	LeasedBy        string          `json:"leased_by,omitempty"`
	LeaseExpiresAt  time.Time       `json:"lease_expires_at,omitempty"`
	HeartbeatAt     time.Time       `json:"heartbeat_at,omitempty"`
	Error           string          `json:"error,omitempty"`
	CancelRequested bool            `json:"cancel_requested,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// Message is an assistant turn, possibly still streaming. Deltas update the
// content in place (State "streaming"); the finished message is State "done".
// User turns (Kind "user") are stored with State "pending" until the controller
// or a running role consumes them as an in-flight injection ("injected").
type Message struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	PlaybookRunID string    `json:"playbook_run_id,omitempty"`
	NodeID        string    `json:"node_id,omitempty"`
	Role          string    `json:"role,omitempty"`
	Model         string    `json:"model,omitempty"`
	Kind          string    `json:"kind"`
	Content       string    `json:"content"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Message kinds and states.
const (
	MessageKindUser      = "user"
	MessageStatePending  = "pending"
	MessageStateInjected = "injected"
)

// Event is an append-only log record.
type Event struct {
	Seq       int64          `json:"seq"`
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Usage records token and cost consumption.
type Usage struct {
	SessionID        string  `json:"session_id"`
	PlaybookRunID    string  `json:"playbook_run_id,omitempty"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}
