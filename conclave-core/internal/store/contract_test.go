package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
	"github.com/texhik/conclave/conclave-core/internal/store/postgres"
)

// TestContract runs the same expectations against every Store implementation.
func TestContract(t *testing.T) {
	impls := []struct {
		name string
		open func(t *testing.T) store.Store
	}{
		{"memory", func(t *testing.T) store.Store { return memory.New() }},
		{"postgres", func(t *testing.T) store.Store {
			dsn := os.Getenv("CORE_TEST_DB_DSN")
			if dsn == "" {
				t.Skip("CORE_TEST_DB_DSN not set; skipping postgres")
			}
			st, err := postgres.Open(context.Background(), dsn)
			if err != nil {
				t.Fatalf("open postgres: %v", err)
			}
			t.Cleanup(func() { st.Close() })
			return st
		}},
	}
	for _, impl := range impls {
		t.Run(impl.name, func(t *testing.T) {
			runContract(t, impl.open(t))
		})
	}
}

func runContract(t *testing.T, st store.Store) {
	ctx := context.Background()
	now := time.Now().UTC()
	tenantID := "t-" + unique()
	if err := st.CreateTenant(ctx, domain.Tenant{ID: tenantID, Name: "Acme", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetTenant(ctx, tenantID); err != nil || got.Name != "Acme" {
		t.Fatalf("GetTenant: %v %+v", err, got)
	}

	userID := "u-" + unique()
	if err := st.CreateUser(ctx, domain.User{ID: userID, TenantID: tenantID, Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	devID := "d-" + unique()
	hash := "hash-" + unique()
	if err := st.CreateDevice(ctx, domain.Device{ID: devID, TenantID: tenantID, UserID: userID, TokenHash: hash}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetDeviceByTokenHash(ctx, hash); err != nil || got.ID != devID {
		t.Fatalf("GetDeviceByTokenHash: %v %+v", err, got)
	}

	utID := "ut-" + unique()
	utHash := "uthash-" + unique()
	if err := st.CreateUserToken(ctx, domain.UserToken{ID: utID, TenantID: tenantID, UserID: userID, TokenHash: utHash}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetUserTokenByHash(ctx, utHash); err != nil || got.ID != utID || got.UserID != userID {
		t.Fatalf("GetUserTokenByHash: %v %+v", err, got)
	}
	if err := st.RevokeUserToken(ctx, utID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetUserTokenByHash(ctx, utHash); got.RevokedAt == nil {
		t.Fatalf("user token not revoked: %+v", got)
	}

	sessID := "s-" + unique()
	if err := st.CreateSession(ctx, domain.Session{ID: sessID, TenantID: tenantID, Title: "Shop", Status: domain.SessionActive, AutoAccept: true, BudgetUSD: 2.5}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionStatus(ctx, sessID, domain.SessionPaused); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetSession(ctx, sessID); got.Status != domain.SessionPaused || !got.AutoAccept || got.BudgetUSD != 2.5 {
		t.Fatalf("session = %+v", got)
	}
	if err := st.SetSessionAgent(ctx, sessID, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if dev, err := st.GetSessionAgent(ctx, sessID); err != nil || dev != "dev-1" {
		t.Fatalf("GetSessionAgent: %v %q", err, dev)
	}

	if err := st.CreateSpec(ctx, domain.Spec{ID: "spec-" + unique(), SessionID: sessID, Version: 1, Content: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSpec(ctx, domain.Spec{ID: "spec-" + unique(), SessionID: sessID, Version: 2, Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	if sp, err := st.LatestSpec(ctx, sessID); err != nil || sp.Version != 2 {
		t.Fatalf("LatestSpec: %v %+v", err, sp)
	}

	planID := "p-" + unique()
	if err := st.CreatePlan(ctx, domain.Plan{ID: planID, SessionID: sessID, Version: 1, Status: domain.PlanActive}); err != nil {
		t.Fatal(err)
	}
	nodeID := "n-" + unique()
	if err := st.CreatePlanNode(ctx, domain.PlanNode{ID: nodeID, PlanID: planID, PlaybookID: "ba", PlaybookVersion: 1, State: domain.NodePending}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePlanNodeState(ctx, nodeID, domain.NodeRunning); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePlanNodeState(ctx, nodeID, domain.NodeDone); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanEdge(ctx, domain.PlanEdge{PlanID: planID, From: nodeID, To: nodeID, Kind: domain.EdgeDependency}); err != nil {
		t.Fatal(err)
	}
	if nodes, err := st.ListPlanNodes(ctx, planID); err != nil || len(nodes) != 1 {
		t.Fatalf("ListPlanNodes: %v %+v", err, nodes)
	}
	if err := st.UpdatePlanNodeOutput(ctx, nodeID, "node output"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetPlanNode(ctx, nodeID); err != nil || got.Output != "node output" || got.Kind != domain.NodeKindPlaybook {
		t.Fatalf("GetPlanNode: %v %+v", err, got)
	}
	if err := st.UpdatePlanStatus(ctx, planID, domain.PlanDone); err != nil {
		t.Fatal(err)
	}
	if p, err := st.GetPlan(ctx, planID); err != nil || p.Status != domain.PlanDone {
		t.Fatalf("UpdatePlanStatus: %v %+v", err, p)
	}

	runID := "r-" + unique()
	if err := st.CreatePlaybookRun(ctx, domain.PlaybookRun{ID: runID, SessionID: sessID, PlanNodeID: nodeID, PlaybookID: "ba", PlaybookVersion: 1, State: domain.NodeRunning}); err != nil {
		t.Fatal(err)
	}
	taskID := "task-" + unique()
	if err := st.CreateTask(ctx, domain.Task{ID: taskID, PlaybookRunID: runID, Title: "do", Tier: "senior", State: domain.TaskPending}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskState(ctx, taskID, domain.TaskRunning, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskState(ctx, taskID, domain.TaskDone, 1); err != nil {
		t.Fatal(err)
	}
	if tasks, err := st.ListTasks(ctx, runID); err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks: %v %+v", err, tasks)
	}

	artID := "a-" + unique()
	if err := st.CreateArtifact(ctx, domain.Artifact{ID: artID, SessionID: sessID, PlaybookRunID: runID, Name: "requirements.md", Kind: "markdown", Ref: "content"}); err != nil {
		t.Fatal(err)
	}
	if arts, err := st.ListArtifacts(ctx, sessID); err != nil || len(arts) != 1 || arts[0].Ref != "content" {
		t.Fatalf("ListArtifacts: %v %+v", err, arts)
	}

	qID := "q-" + unique()
	if err := st.CreateQuestion(ctx, domain.Question{
		ID: qID, SessionID: sessID, Kind: domain.QuestionKindInterview, Ref: "interview-1",
		Text: "Which DB?", Options: []domain.Option{{Label: "Postgres", Recommended: true}},
		AutoPolicy: domain.AutoInherit, State: domain.QuestionOpen,
	}); err != nil {
		t.Fatal(err)
	}
	if q, err := st.GetQuestion(ctx, qID); err != nil || len(q.Options) != 1 || !q.Options[0].Recommended ||
		q.Kind != domain.QuestionKindInterview || q.Ref != "interview-1" {
		t.Fatalf("GetQuestion: %v %+v", err, q)
	}

	inID := "iv-" + unique()
	if err := st.CreateInterview(ctx, domain.Interview{
		ID: inID, SessionID: sessID, Idea: "build", State: domain.InterviewWaiting,
		PendingQuestionID: qID, Transcript: []domain.InterviewMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if in, err := st.GetInterview(ctx, inID); err != nil || in.State != domain.InterviewWaiting ||
		len(in.Transcript) != 1 || in.PendingQuestionID != qID {
		t.Fatalf("GetInterview: %v %+v", err, in)
	}
	in, _ := st.GetInterview(ctx, inID)
	in.State = domain.InterviewDone
	in.Transcript = append(in.Transcript, domain.InterviewMessage{Role: "assistant", Content: "spec"})
	if err := st.UpdateInterview(ctx, *in); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetInterview(ctx, inID); got.State != domain.InterviewDone || len(got.Transcript) != 2 {
		t.Fatalf("UpdateInterview: %+v", got)
	}
	if err := st.UpdateQuestionState(ctx, qID, domain.QuestionAnswered); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAnswer(ctx, domain.Answer{ID: "ans-" + unique(), QuestionID: qID, Selected: []string{"Postgres"}}); err != nil {
		t.Fatal(err)
	}
	if a, err := st.GetAnswer(ctx, qID); err != nil || len(a.Selected) != 1 {
		t.Fatalf("GetAnswer: %v %+v", err, a)
	}

	seq1, err := st.AppendEvent(ctx, domain.Event{ID: "e1-" + unique(), SessionID: sessID, Type: "a"})
	if err != nil {
		t.Fatal(err)
	}
	seq2, err := st.AppendEvent(ctx, domain.Event{ID: "e2-" + unique(), SessionID: sessID, Type: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if seq2 <= seq1 {
		t.Fatalf("seq not increasing: %d then %d", seq1, seq2)
	}
	evs, err := st.ListEvents(ctx, sessID, seq1)
	if err != nil || len(evs) != 1 || evs[0].Type != "b" {
		t.Fatalf("ListEvents: %v %+v", err, evs)
	}

	if err := st.RecordUsage(ctx, domain.Usage{SessionID: sessID, PromptTokens: 10, CompletionTokens: 5, CostUSD: 0.01}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordUsage(ctx, domain.Usage{SessionID: sessID, PromptTokens: 2}); err != nil {
		t.Fatal(err)
	}
	if u, err := st.TotalUsage(ctx, sessID); err != nil || u.PromptTokens != 12 || u.CompletionTokens != 5 {
		t.Fatalf("TotalUsage: %v %+v", err, u)
	}

	msgID := "msg-" + unique()
	if err := st.UpsertMessage(ctx, domain.Message{
		ID: msgID, SessionID: sessID, Role: "senior", Kind: "assistant", Content: "partial", State: "streaming",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMessage(ctx, domain.Message{
		ID: msgID, SessionID: sessID, Role: "senior", Kind: "assistant", Content: "partial more", State: "streaming",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeMessage(ctx, msgID, "final answer"); err != nil {
		t.Fatal(err)
	}
	if msgs, err := st.ListMessages(ctx, sessID, 10); err != nil || len(msgs) != 1 ||
		msgs[0].State != "done" || msgs[0].Content != "final answer" {
		t.Fatalf("ListMessages: %v %+v", err, msgs)
	}

	// In-flight user messages: pending until consumed.
	userMsgID := "umsg-" + unique()
	if err := st.UpsertMessage(ctx, domain.Message{
		ID: userMsgID, SessionID: sessID, Kind: domain.MessageKindUser,
		Content: "please clarify", State: domain.MessageStatePending,
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err := st.PendingMessages(ctx, sessID); err != nil || len(pending) != 1 || pending[0].ID != userMsgID {
		t.Fatalf("PendingMessages: %v %+v", err, pending)
	}
	if err := st.MarkMessagesInjected(ctx, []string{userMsgID}); err != nil {
		t.Fatal(err)
	}
	if pending, err := st.PendingMessages(ctx, sessID); err != nil || len(pending) != 0 {
		t.Fatalf("PendingMessages after inject: %v %+v", err, pending)
	}

	jobID := "job-" + unique()
	if err := st.EnqueueJob(ctx, domain.Job{
		ID: jobID, Kind: domain.JobInterview, SessionID: sessID, PlanID: planID,
		Payload: []byte(`{"mode":"start","idea":"x"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if j, err := st.GetJob(ctx, jobID); err != nil || j.State != domain.JobQueued {
		t.Fatalf("GetJob: %v %+v", err, j)
	}
	claimed, err := st.ClaimJob(ctx, "w1", time.Minute)
	if err != nil || claimed == nil || claimed.ID != jobID || claimed.State != domain.JobLeased || claimed.Attempts != 1 {
		t.Fatalf("ClaimJob: %v %+v", err, claimed)
	}
	if string(claimed.Payload) != `{"mode":"start","idea":"x"}` {
		t.Fatalf("job payload = %q", string(claimed.Payload))
	}
	if err := st.HeartbeatJob(ctx, claimed.ID, "w1", time.Minute); err != nil {
		t.Fatalf("HeartbeatJob: %v", err)
	}
	if err := st.HeartbeatJob(ctx, claimed.ID, "w2", time.Minute); err != store.ErrNotFound {
		t.Fatalf("stale heartbeat = %v, want ErrNotFound", err)
	}
	if err := st.CompleteJob(ctx, claimed.ID, domain.JobDone, ""); err != nil {
		t.Fatal(err)
	}
	if j, err := st.GetJob(ctx, jobID); err != nil || j.State != domain.JobDone {
		t.Fatalf("after complete: %v %+v", err, j)
	}
	if jobs, err := st.ListJobs(ctx, sessID, 10); err != nil || len(jobs) != 1 {
		t.Fatalf("ListJobs: %v %+v", err, jobs)
	}

	// --- Configuration (hot-reloadable) ---
	v0, err := st.ConfigVersion(ctx)
	if err != nil {
		t.Fatalf("ConfigVersion: %v", err)
	}
	provID := "prov-" + unique()
	if err := st.UpsertProvider(ctx, domain.Provider{
		ID: provID, Name: "prov-" + unique(), BaseURL: "http://llm", APIKey: "enc", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetProvider(ctx, provID); err != nil || got.BaseURL != "http://llm" || got.APIKey != "enc" {
		t.Fatalf("GetProvider: %v %+v", err, got)
	}
	if _, err := st.GetProvider(ctx, "missing"); err != store.ErrNotFound {
		t.Fatalf("GetProvider missing = %v", err)
	}
	modelID := "model-" + unique()
	if err := st.UpsertProviderModel(ctx, domain.ProviderModel{
		ID: modelID, ProviderID: provID, Name: "m1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetProviderModel(ctx, modelID); err != nil || got.Name != "m1" {
		t.Fatalf("GetProviderModel: %v %+v", err, got)
	}
	roleID := "role-" + unique()
	if err := st.UpsertRole(ctx, domain.Role{
		ID: roleID, Key: "role-" + unique(), Title: "Backend", Control: "supervisor", Inputs: []string{"spec"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetRole(ctx, roleID); err != nil || got.Title != "Backend" || len(got.Inputs) != 1 {
		t.Fatalf("GetRole: %v %+v", err, got)
	}
	gradeID := "grade-" + unique()
	if err := st.UpsertRoleGrade(ctx, domain.RoleGrade{
		ID: gradeID, RoleID: roleID, Grade: "senior", Rank: 2, ModelID: modelID, Tools: []string{"read_file"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if gs, err := st.ListRoleGrades(ctx); err != nil || len(gs) == 0 {
		t.Fatalf("ListRoleGrades: %v %+v", err, gs)
	}
	if err := st.UpsertBrainConfig(ctx, domain.BrainConfig{Name: domain.BrainController, ModelID: modelID}); err != nil {
		t.Fatal(err)
	}
	if b, err := st.GetBrainConfig(ctx, domain.BrainController); err != nil || b.ModelID != modelID {
		t.Fatalf("GetBrainConfig: %v %+v", err, b)
	}
	if v1, err := st.ConfigVersion(ctx); err != nil || v1 <= v0 {
		t.Fatalf("ConfigVersion did not advance: %v %d -> %d", err, v0, v1)
	}
	if err := st.DeleteRoleGrade(ctx, gradeID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRole(ctx, roleID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBrainConfig(ctx, domain.BrainController); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProviderModel(ctx, modelID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProvider(ctx, provID); err != nil {
		t.Fatal(err)
	}

	// Deleting a session removes it and all its child records.
	if err := st.DeleteSession(ctx, sessID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.GetSession(ctx, sessID); err != store.ErrNotFound {
		t.Fatalf("GetSession after delete = %v", err)
	}
}

var counter int

func unique() string {
	counter++
	return time.Now().Format("150405.000000000") + "-" + string(rune('a'+counter%26))
}
