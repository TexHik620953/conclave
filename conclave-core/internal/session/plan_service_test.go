package session

import (
	"context"
	"strings"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func newPlanService(t *testing.T) (*Service, *memory.Store) {
	t.Helper()
	st := memory.New()
	log := events.New(st)
	registry, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	sup := &orchestrator.Supervisor{
		Brain:    &orchestrator.FakeBrain{Finish: "done"},
		Roles:    &orchestrator.FakeRoleRunner{},
		Log:      log,
		MaxSteps: 5,
	}
	return NewService(st, log, registry, sup), st
}

func TestPlanGateWaitingAndResume(t *testing.T) {
	ctx := context.Background()
	svc, st := newPlanService(t)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t", Title: "plan"})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := svc.CreatePlan(ctx, CreatePlanInput{
		SessionID: sess.ID,
		Nodes: []PlanNodeSpec{
			{ID: "a", PlaybookID: "ba"},
			{ID: "g", Kind: "gate", Title: "Approve the requirements?"},
			{ID: "b", PlaybookID: "general"},
		},
		Edges: []PlanEdgeSpec{
			{From: "a", To: "g", Kind: "dependency"},
			{From: "a", To: "b", Kind: "dependency"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := svc.RunPlan(ctx, sess.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "waiting" || len(report.Waiting) != 1 || report.Waiting[0] != "g" {
		t.Fatalf("report = %+v, want waiting on g", report)
	}

	// Sibling b ran; gate g waits.
	if n, _ := st.GetPlanNodeByKey(ctx, plan.ID, "b"); n.State != domain.NodeDone {
		t.Fatalf("b state = %s", n.State)
	}
	if n, _ := st.GetPlanNodeByKey(ctx, plan.ID, "g"); n.State != domain.NodeWaitingUser {
		t.Fatalf("g state = %s", n.State)
	}

	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 1 || qs[0].State != domain.QuestionOpen {
		t.Fatalf("questions = %+v", qs)
	}

	job, err := svc.AnswerQuestion(ctx, qs[0].ID, []string{"Approve"}, "")
	if err != nil || job == nil {
		t.Fatalf("answer: %v job=%+v", err, job)
	}
	report, err = svc.RunPlan(ctx, sess.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || report.Status != "done" {
		t.Fatalf("resumed report = %+v", report)
	}
}

func TestPlanAutoAccept(t *testing.T) {
	ctx := context.Background()
	svc, st := newPlanService(t)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t", AutoAccept: true})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.CreatePlan(ctx, CreatePlanInput{
		SessionID: sess.ID,
		Nodes: []PlanNodeSpec{
			{ID: "a", PlaybookID: "ba"},
			{ID: "g", Kind: "gate", Title: "ok?"},
		},
		Edges: []PlanEdgeSpec{{From: "a", To: "g"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := svc.RunPlan(ctx, sess.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "done" {
		t.Fatalf("status = %s", report.Status)
	}
	// The question was auto-answered.
	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 1 || qs[0].State != domain.QuestionAnswered {
		t.Fatalf("questions = %+v", qs)
	}
	a, err := st.GetAnswer(ctx, qs[0].ID)
	if err != nil || !a.Auto || len(a.Selected) != 1 || a.Selected[0] != "Approve" {
		t.Fatalf("answer = %+v err=%v", a, err)
	}
}

func TestPlanDataEdgePassesInputs(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)
	registry, _ := playbook.LoadBuiltin()
	sup := &orchestrator.Supervisor{
		Brain: &orchestrator.FakeBrain{
			Decisions: []orchestrator.Decision{{Delegate: &orchestrator.Delegation{Role: "senior", Task: "use upstream"}}},
			Finish:    "result",
		},
		Roles:    &orchestrator.FakeRoleRunner{},
		Log:      log,
		MaxSteps: 3,
	}
	svc := NewService(st, log, registry, sup)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sess, _ := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t"})
	plan, err := svc.CreatePlan(ctx, CreatePlanInput{
		SessionID: sess.ID,
		Nodes:     []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}, {ID: "b", PlaybookID: "general"}},
		Edges:     []PlanEdgeSpec{{From: "a", To: "b", Kind: "data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunPlan(ctx, sess.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	// The downstream node's supervisor saw the upstream output as an input.
	seen := false
	for _, in := range sup.Brain.(*orchestrator.FakeBrain).Seen() {
		if strings.Contains(in.Spec, "Inputs from upstream") && strings.Contains(in.Spec, "result") {
			seen = true
		}
	}
	if !seen {
		t.Fatal("downstream node did not receive upstream inputs")
	}
}

func TestSessionPauseResume(t *testing.T) {
	ctx := context.Background()
	svc, st := newPlanService(t)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sess, _ := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t"})
	plan, err := svc.CreatePlan(ctx, CreatePlanInput{
		SessionID: sess.ID,
		Nodes:     []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunPlan(ctx, sess.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.PauseSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.GetSession(ctx, sess.ID); s.Status != domain.SessionPaused {
		t.Fatalf("status = %s", s.Status)
	}
	report, err := svc.ResumeSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || report.Status != "done" {
		t.Fatalf("report = %+v", report)
	}
	if s, _ := st.GetSession(ctx, sess.ID); s.Status != domain.SessionActive {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestRevisePlanPreservesState(t *testing.T) {
	ctx := context.Background()
	svc, st := newPlanService(t)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sess, _ := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t"})

	plan1, err := svc.CreatePlan(ctx, CreatePlanInput{
		SessionID: sess.ID,
		Nodes:     []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunPlan(ctx, sess.ID, plan1.ID); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetPlanNodeByKey(ctx, plan1.ID, "a")
	if a.State != domain.NodeDone || a.Output == "" {
		t.Fatalf("node a = %+v", a)
	}

	plan2, err := svc.RevisePlan(ctx, sess.ID, plan1.ID, CreatePlanInput{
		Nodes: []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}, {ID: "c", PlaybookID: "general"}},
		Edges: []PlanEdgeSpec{{From: "a", To: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Version != 2 {
		t.Fatalf("version = %d, want 2", plan2.Version)
	}
	if base, _ := st.GetPlan(ctx, plan1.ID); base.Status != domain.PlanRevised {
		t.Fatalf("base status = %s", base.Status)
	}
	a2, _ := st.GetPlanNodeByKey(ctx, plan2.ID, "a")
	if a2.State != domain.NodeDone || a2.Output != a.Output {
		t.Fatalf("kept node a lost state: %+v", a2)
	}
	c, _ := st.GetPlanNodeByKey(ctx, plan2.ID, "c")
	if c.State != domain.NodePending {
		t.Fatalf("new node c = %s", c.State)
	}

	// Running the revised plan executes only the new node.
	report, err := svc.RunPlan(ctx, sess.ID, plan2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "done" {
		t.Fatalf("report = %+v", report)
	}
	c, _ = st.GetPlanNodeByKey(ctx, plan2.ID, "c")
	if c.State != domain.NodeDone {
		t.Fatalf("node c = %s", c.State)
	}
}

func TestRevisePlanVersionLimit(t *testing.T) {
	ctx := context.Background()
	svc, st := newPlanService(t)
	svc.MaxPlanVersions = 1
	_ = st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive})
	plan, err := svc.CreatePlan(ctx, CreatePlanInput{SessionID: "s", Nodes: []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RevisePlan(ctx, "s", plan.ID, CreatePlanInput{Nodes: []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}}}); err == nil {
		t.Fatal("expected version limit error")
	}
}
