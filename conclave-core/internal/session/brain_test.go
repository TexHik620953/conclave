package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func newBrainService(t *testing.T, responses []llmgw.Response) (*Service, *memory.Store) {
	t.Helper()
	st := memory.New()
	log := events.New(st)
	registry, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	sup := &orchestrator.Supervisor{
		Brain: &orchestrator.FakeBrain{
			Decisions: []orchestrator.Decision{{Delegate: &orchestrator.Delegation{Role: "senior", Task: "do"}}},
			Finish:    "# done",
		},
		Roles:    &orchestrator.FakeRoleRunner{},
		Log:      log,
		MaxSteps: 5,
	}
	svc := NewService(st, log, registry, sup)
	i := 0
	svc.Controller = NewController(svc, &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		if i >= len(responses) {
			return llmgw.Response{Message: llmgw.Message{Role: "assistant", Content: "ok"}}
		}
		r := responses[i]
		i++
		return r
	}}, "m")
	return svc, st
}

func toolCall(name, args string) llmgw.Response {
	return llmgw.Response{Message: llmgw.Message{
		Role:      "assistant",
		ToolCalls: []llmgw.ToolCall{{ID: "1", Name: name, Arguments: args}},
	}}
}

// TestControllerBrainFlow drives the single brain from a goal through a
// clarifying question, a spec, a plan and finish.
func TestControllerBrainFlow(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, []llmgw.Response{
		toolCall("ask", `{"question":"Which stack?","options":["Go","Node"]}`),
		toolCall("write_spec", `{"content":"# Spec\n- use Go"}`),
		toolCall("set_plan", `{"nodes":[{"id":"a","playbook_id":"ba"}],"edges":[]}`),
		toolCall("finish", `{"summary":"done"}`),
	})
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "shop"})
	if err != nil {
		t.Fatal(err)
	}

	// Goal: idle session, so a controller job is queued and the text is pending.
	job, injected, err := svc.Goal(ctx, sess.ID, "build a shop")
	if err != nil || injected || job == nil {
		t.Fatalf("goal job=%v injected=%v err=%v", job, injected, err)
	}
	if job.Kind != domain.JobSession {
		t.Fatalf("job kind = %s", job.Kind)
	}
	if msgs, _ := st.PendingMessages(ctx, sess.ID); len(msgs) != 1 {
		t.Fatalf("pending = %+v", msgs)
	}

	// The brain asks a clarifying question.
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 1 || qs[0].Kind != domain.QuestionKindController || qs[0].State != domain.QuestionOpen {
		t.Fatalf("questions = %+v", qs)
	}
	if len(qs[0].Options) != 2 || qs[0].Text == "" {
		t.Fatalf("question options/text = %+v", qs[0])
	}
	evs, _ := st.ListEvents(ctx, sess.ID, 0)
	foundAsked := false
	for _, e := range evs {
		if e.Type == domain.EventQuestionAsked {
			foundAsked = true
			if e.Payload["text"] == "" || e.Payload["question_id"] == "" {
				t.Fatalf("question.asked payload = %+v", e.Payload)
			}
		}
	}
	if !foundAsked {
		t.Fatal("no question.asked event")
	}
	qid := qs[0].ID
	if msgs, _ := st.PendingMessages(ctx, sess.ID); len(msgs) != 0 {
		t.Fatalf("pending not consumed: %+v", msgs)
	}

	// Answering resumes the brain: it writes the spec and builds the plan.
	if _, err := svc.AnswerControllerQuestion(ctx, qid, []string{"Go"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	spec, err := st.LatestSpec(ctx, sess.ID)
	if err != nil || spec.Content == "" {
		t.Fatalf("spec = %+v err=%v", spec, err)
	}
	plan, err := st.ActivePlan(ctx, sess.ID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	nodes, _ := st.ListPlanNodes(ctx, plan.ID)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v", nodes)
	}
	jobs, _ := st.ListJobs(ctx, sess.ID, 100)
	var runQueued bool
	for _, j := range jobs {
		if j.Kind == domain.JobRunPlan && j.State == domain.JobQueued {
			runQueued = true
		}
	}
	if !runQueued {
		t.Fatalf("run_plan not queued: %+v", jobs)
	}

	// After the plan completes the brain reviews and finishes.
	if err := st.UpdatePlanStatus(ctx, plan.ID, domain.PlanDone); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	sess, _ = st.GetSession(ctx, sess.ID)
	if sess.Status != domain.SessionDone {
		t.Fatalf("session status = %s", sess.Status)
	}
}

// TestControllerGeneratesTitle checks the session title is summarized from the
// first user message.
func TestControllerGeneratesTitle(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, []llmgw.Response{
		{Message: llmgw.Message{Role: "assistant", Content: "Todo App in Go"}},
		toolCall("ask", `{"question":"Any deadline?"}`),
	})
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Goal(ctx, sess.ID, "please build a small todo app in Go"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSession(ctx, sess.ID)
	if got.Title != "Todo App in Go" {
		t.Fatalf("title = %q", got.Title)
	}
}

// TestControllerRequiresSpecBeforePlan checks the discovery guard: with no spec
// the controller may not build a plan and instead asks the user.
func TestControllerRequiresSpecBeforePlan(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, []llmgw.Response{
		toolCall("set_plan", `{"nodes":[{"id":"a","playbook_id":"architecture","grade":"senior"}],"edges":[]}`),
	})
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "vpn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Goal(ctx, sess.ID, "build a vpn mesh with sales in telegram"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActivePlan(ctx, sess.ID); err == nil {
		t.Fatal("plan must not be created before a spec exists")
	}
	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 1 || qs[0].State != domain.QuestionOpen {
		t.Fatalf("expected a clarifying question, got %+v", qs)
	}
}

// TestControllerQuestionnaire checks a batch ask creates one card per question.
func TestControllerQuestionnaire(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, []llmgw.Response{
		toolCall("ask", `{"questions":[{"question":"What do we sell?","options":["VPN access","White-label"]},{"question":"Who are the users?","options":["Individuals","Companies"]}]}`),
	})
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "vpn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Goal(ctx, sess.ID, "vpn mesh with sales in telegram"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 2 {
		t.Fatalf("questions = %d, want 2", len(qs))
	}
	for _, q := range qs {
		if q.Kind != domain.QuestionKindController || len(q.Options) != 2 {
			t.Fatalf("question = %+v", q)
		}
	}
}

func TestFormatQuestionText(t *testing.T) {
	in := "Уточните: (1) что продаём? (2) кто пользователи? (3) монетизация?"
	out := formatQuestionText(in)
	if !strings.Contains(out, "\n(1)") || !strings.Contains(out, "\n(2)") || !strings.Contains(out, "\n(3)") {
		t.Fatalf("no line breaks added: %q", out)
	}
	if formatQuestionText("already\nmultiline") != "already\nmultiline" {
		t.Fatal("multiline text must be left as is")
	}
}

// TestAnswerQuestionsBatch resumes the controller only after all questions are
// answered (one job), not one job per answer.
func TestAnswerQuestionsBatch(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, []llmgw.Response{
		toolCall("ask", `{"questions":[{"question":"Q1","options":["a","b"]},{"question":"Q2","options":["c","d"]}]}`),
	})
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Goal(ctx, sess.ID, "hi"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 2 {
		t.Fatalf("questions = %d", len(qs))
	}

	job, err := svc.AnswerQuestions(ctx, sess.ID, []QuestionAnswer{{QuestionID: qs[0].ID, Selected: []string{"a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatal("must not enqueue while another question is open")
	}
	job, err = svc.AnswerQuestions(ctx, sess.ID, []QuestionAnswer{{QuestionID: qs[1].ID, Selected: []string{"c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.Kind != domain.JobSession {
		t.Fatalf("expected one session job after all answered, got %+v", job)
	}
}

// TestInterruptCancelsQueuedJob checks that interrupting cancels a queued job
// immediately so no worker runs it.
func TestInterruptCancelsQueuedJob(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, nil)
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.EnqueueSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.InterruptSession(ctx, sess.ID)
	if err != nil || n < 1 {
		t.Fatalf("interrupt n=%d err=%v", n, err)
	}
	got, _ := st.GetJob(ctx, job.ID)
	if got == nil || got.State != domain.JobCancelled {
		t.Fatalf("job = %+v, want cancelled", got)
	}
	claimed, err := st.ClaimJob(ctx, "w", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("cancelled job was claimed: %+v", claimed)
	}
}

// TestControllerDoesNotFinishEarly checks a premature finish (or plain text) on
// a fresh session becomes a clarification instead of ending the session.
func TestControllerDoesNotFinishEarly(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, []llmgw.Response{
		toolCall("finish", `{"summary":"all done"}`),
	})
	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "vpn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Goal(ctx, sess.ID, "vpn mesh with sales in telegram"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Controller.Step(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSession(ctx, sess.ID)
	if got.Status == domain.SessionDone {
		t.Fatal("session must not finish on the first turn")
	}
	qs, _ := st.ListQuestions(ctx, sess.ID)
	if len(qs) != 1 || qs[0].State != domain.QuestionOpen {
		t.Fatalf("expected a clarification, got %+v", qs)
	}
}

// TestGoalInjectsIntoRunningWork verifies that a message sent while work is
// active is queued durably instead of starting a new controller job.
func TestGoalInjectsIntoRunningWork(t *testing.T) {
	ctx := context.Background()
	svc, st := newBrainService(t, nil)
	sess, _ := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "x"})
	if _, err := svc.EnqueueSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	job, injected, err := svc.Goal(ctx, sess.ID, "also add tests")
	if err != nil {
		t.Fatal(err)
	}
	if !injected || job != nil {
		t.Fatalf("job=%v injected=%v, want injected", job, injected)
	}
	msgs, _ := st.PendingMessages(ctx, sess.ID)
	if len(msgs) != 1 || msgs[0].Content != "also add tests" {
		t.Fatalf("pending = %+v", msgs)
	}

	// The inbox drain consumes the message exactly once.
	drain := svc.InboxDrain(sess.ID)
	got := drain(ctx)
	if len(got) != 1 || got[0] != "also add tests" {
		t.Fatalf("drain = %+v", got)
	}
	if again := drain(ctx); len(again) != 0 {
		t.Fatalf("drain not idempotent: %+v", again)
	}
}
