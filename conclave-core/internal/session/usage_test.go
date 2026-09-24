package session

import (
	"context"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

type fixedClient struct{ usage llmgw.Usage }

func (f fixedClient) Complete(context.Context, llmgw.Request) (llmgw.Response, error) {
	return llmgw.Response{Usage: f.usage}, nil
}

func TestUsageRecorderRecordsAndEnforcesBudget(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sid := "s"
	if err := st.CreateSession(ctx, domain.Session{ID: sid, TenantID: "t", Status: domain.SessionActive, BudgetUSD: 0.001}); err != nil {
		t.Fatal(err)
	}
	u := &UsageRecorder{
		Client: fixedClient{usage: llmgw.Usage{PromptTokens: 1000}}, // 1000 * $1/1M = $0.001
		Store:  st, PriceIn: 1.0,
	}
	callCtx := WithSessionID(ctx, sid)

	if _, err := u.Complete(callCtx, llmgw.Request{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	total, _ := st.TotalUsage(ctx, sid)
	if total.PromptTokens != 1000 || total.CostUSD != 0.001 {
		t.Fatalf("usage = %+v", total)
	}
	// The second call pushes cost over the budget.
	if _, err := u.Complete(callCtx, llmgw.Request{}); err == nil {
		t.Fatal("expected budget error on second call")
	}
}

func TestUsageRecorderNoSession(t *testing.T) {
	u := &UsageRecorder{Client: fixedClient{usage: llmgw.Usage{PromptTokens: 5}}, Store: memory.New()}
	if _, err := u.Complete(context.Background(), llmgw.Request{}); err != nil {
		t.Fatalf("no-session call should not error: %v", err)
	}
}
