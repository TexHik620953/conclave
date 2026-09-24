package session

import (
	"context"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func toolResponse(name, args string) llmgw.Response {
	return llmgw.Response{Message: llmgw.Message{
		Role:      "assistant",
		ToolCalls: []llmgw.ToolCall{{ID: "1", Name: name, Arguments: args}},
	}}
}

func TestInterviewerAsksThenWritesSpec(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	client := &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		calls++
		if calls == 1 {
			return toolResponse("ask", `{"question":"Which database?","options":["Postgres","MySQL"]}`)
		}
		return toolResponse("write_spec", `{"content":"# Spec\n- use Postgres"}`)
	}}
	iv := NewInterviewer(st, log, client, "m")

	in, q, err := iv.Start(ctx, "s", "build an online shop")
	if err != nil {
		t.Fatal(err)
	}
	if q == nil || q.Kind != domain.QuestionKindInterview || len(q.Options) != 2 || !q.Options[0].Recommended {
		t.Fatalf("question = %+v", q)
	}
	if in.State != domain.InterviewWaiting || in.PendingQuestionID != q.ID {
		t.Fatalf("interview = %+v", in)
	}

	in2, q2, err := iv.Resume(ctx, in.ID, []string{"Postgres"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if q2 != nil {
		t.Fatalf("unexpected follow-up question: %+v", q2)
	}
	if in2.State != domain.InterviewDone || in2.SpecID == "" {
		t.Fatalf("interview = %+v", in2)
	}
	spec, err := st.LatestSpec(ctx, "s")
	if err != nil || spec.Version != 1 {
		t.Fatalf("spec = %+v err=%v", spec, err)
	}
}

func TestInterviewerPlainContentBecomesSpec(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)
	_ = st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive})
	client := &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		return llmgw.Response{Message: llmgw.Message{Role: "assistant", Content: "# Spec from content"}}
	}}
	iv := NewInterviewer(st, log, client, "m")
	in, q, err := iv.Start(ctx, "s", "idea")
	if err != nil {
		t.Fatal(err)
	}
	if q != nil || in.State != domain.InterviewDone {
		t.Fatalf("in=%+v q=%+v", in, q)
	}
}
