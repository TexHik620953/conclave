// Package store defines the persistence contract for conclave-core.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
)

// ErrNotFound is returned when an entity does not exist.
var ErrNotFound = errors.New("not found")

// Store is the persistence contract. Implementations: memory (tests/dev) and
// postgres (production).
type Store interface {
	// Tenancy and identity.
	CreateTenant(ctx context.Context, t domain.Tenant) error
	GetTenant(ctx context.Context, id string) (*domain.Tenant, error)
	CreateUser(ctx context.Context, u domain.User) error
	GetUser(ctx context.Context, id string) (*domain.User, error)
	CreateDevice(ctx context.Context, d domain.Device) error
	GetDevice(ctx context.Context, id string) (*domain.Device, error)
	GetDeviceByTokenHash(ctx context.Context, hash string) (*domain.Device, error)
	TouchDevice(ctx context.Context, id string) error
	RevokeDevice(ctx context.Context, id string) error

	// User tokens (user-scoped, for the client WebSocket).
	CreateUserToken(ctx context.Context, t domain.UserToken) error
	GetUserTokenByHash(ctx context.Context, hash string) (*domain.UserToken, error)
	RevokeUserToken(ctx context.Context, id string) error

	// Projects and sessions.
	CreateProject(ctx context.Context, p domain.Project) error
	GetProject(ctx context.Context, id string) (*domain.Project, error)

	// Session -> device binding for agent tool execution.
	SetSessionAgent(ctx context.Context, sessionID, deviceID string) error
	GetSessionAgent(ctx context.Context, sessionID string) (string, error)
	CreateSession(ctx context.Context, s domain.Session) error
	GetSession(ctx context.Context, id string) (*domain.Session, error)
	UpdateSessionStatus(ctx context.Context, id string, status domain.SessionStatus) error
	UpdateSessionTitle(ctx context.Context, id, title string) error
	ListSessions(ctx context.Context, tenantID string, limit int) ([]domain.Session, error)
	DeleteSession(ctx context.Context, id string) error

	// Specs and plans.
	CreateSpec(ctx context.Context, s domain.Spec) error
	LatestSpec(ctx context.Context, sessionID string) (*domain.Spec, error)
	CreatePlan(ctx context.Context, p domain.Plan) error
	GetPlan(ctx context.Context, id string) (*domain.Plan, error)
	ActivePlan(ctx context.Context, sessionID string) (*domain.Plan, error)
	UpdatePlanStatus(ctx context.Context, id string, status domain.PlanStatus) error
	CreatePlanNode(ctx context.Context, n domain.PlanNode) error
	GetPlanNode(ctx context.Context, id string) (*domain.PlanNode, error)
	GetPlanNodeByKey(ctx context.Context, planID, key string) (*domain.PlanNode, error)
	UpdatePlanNodeState(ctx context.Context, id string, state domain.NodeState) error
	UpdatePlanNodeOutput(ctx context.Context, id, output string) error
	ListPlanNodes(ctx context.Context, planID string) ([]domain.PlanNode, error)
	CreatePlanEdge(ctx context.Context, e domain.PlanEdge) error
	ListPlanEdges(ctx context.Context, planID string) ([]domain.PlanEdge, error)

	// Playbook runs and tasks.
	CreatePlaybookRun(ctx context.Context, r domain.PlaybookRun) error
	GetPlaybookRun(ctx context.Context, id string) (*domain.PlaybookRun, error)
	UpdatePlaybookRunState(ctx context.Context, id string, state domain.NodeState) error
	CreateTask(ctx context.Context, t domain.Task) error
	UpdateTaskState(ctx context.Context, id string, state domain.TaskState, attempts int) error
	ListTasks(ctx context.Context, playbookRunID string) ([]domain.Task, error)
	CreateAttempt(ctx context.Context, a domain.Attempt) error

	// Artifacts, questions and answers.
	CreateArtifact(ctx context.Context, a domain.Artifact) error
	ListArtifacts(ctx context.Context, sessionID string) ([]domain.Artifact, error)
	CreateQuestion(ctx context.Context, q domain.Question) error
	GetQuestion(ctx context.Context, id string) (*domain.Question, error)
	UpdateQuestionState(ctx context.Context, id string, state domain.QuestionState) error
	ListQuestions(ctx context.Context, sessionID string) ([]domain.Question, error)
	CreateAnswer(ctx context.Context, a domain.Answer) error
	GetAnswer(ctx context.Context, questionID string) (*domain.Answer, error)

	// Interviews.
	CreateInterview(ctx context.Context, in domain.Interview) error
	GetInterview(ctx context.Context, id string) (*domain.Interview, error)
	UpdateInterview(ctx context.Context, in domain.Interview) error

	// Messages (assistant turns, including streaming partial content).
	UpsertMessage(ctx context.Context, m domain.Message) error
	FinalizeMessage(ctx context.Context, id, content string) error
	ListMessages(ctx context.Context, sessionID string, limit int) ([]domain.Message, error)
	// PendingMessages returns un-consumed user messages (in-flight inbox).
	PendingMessages(ctx context.Context, sessionID string) ([]domain.Message, error)
	// MarkMessagesInjected marks user messages as consumed by the brain/roles.
	MarkMessagesInjected(ctx context.Context, ids []string) error

	// Jobs (durable execution queue).
	EnqueueJob(ctx context.Context, j domain.Job) error
	GetJob(ctx context.Context, id string) (*domain.Job, error)
	ClaimJob(ctx context.Context, workerID string, lease time.Duration) (*domain.Job, error)
	HeartbeatJob(ctx context.Context, id, workerID string, lease time.Duration) error
	CompleteJob(ctx context.Context, id string, state domain.JobState, errMsg string) error
	RequestJobCancel(ctx context.Context, id string) error
	JobCancelRequested(ctx context.Context, id string) (bool, error)
	ListJobs(ctx context.Context, sessionID string, limit int) ([]domain.Job, error)

	// Event log and usage.
	AppendEvent(ctx context.Context, e domain.Event) (int64, error)
	ListEvents(ctx context.Context, sessionID string, fromSeq int64) ([]domain.Event, error)
	RecordUsage(ctx context.Context, u domain.Usage) error
	TotalUsage(ctx context.Context, sessionID string) (domain.Usage, error)

	// Configuration (DB is the source of truth; every replica hot-reloads).
	ConfigVersion(ctx context.Context) (int64, error)
	ListProviders(ctx context.Context) ([]domain.Provider, error)
	GetProvider(ctx context.Context, id string) (*domain.Provider, error)
	GetProviderByName(ctx context.Context, name string) (*domain.Provider, error)
	UpsertProvider(ctx context.Context, p domain.Provider) error
	DeleteProvider(ctx context.Context, id string) error
	ListProviderModels(ctx context.Context) ([]domain.ProviderModel, error)
	GetProviderModel(ctx context.Context, id string) (*domain.ProviderModel, error)
	UpsertProviderModel(ctx context.Context, m domain.ProviderModel) error
	DeleteProviderModel(ctx context.Context, id string) error
	ListRoles(ctx context.Context) ([]domain.Role, error)
	GetRole(ctx context.Context, id string) (*domain.Role, error)
	UpsertRole(ctx context.Context, r domain.Role) error
	DeleteRole(ctx context.Context, id string) error
	ListRoleGrades(ctx context.Context) ([]domain.RoleGrade, error)
	UpsertRoleGrade(ctx context.Context, g domain.RoleGrade) error
	DeleteRoleGrade(ctx context.Context, id string) error
	ListBrainConfigs(ctx context.Context) ([]domain.BrainConfig, error)
	GetBrainConfig(ctx context.Context, name string) (*domain.BrainConfig, error)
	UpsertBrainConfig(ctx context.Context, b domain.BrainConfig) error
	DeleteBrainConfig(ctx context.Context, name string) error

	// Close releases resources.
	Close() error
}
