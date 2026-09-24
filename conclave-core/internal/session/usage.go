package session

import (
	"context"
	"fmt"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

// WithSessionID attaches the session id to a context so the usage recorder can
// attribute token usage and enforce budgets.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return stream.WithSessionID(ctx, sessionID)
}

// SessionIDFrom returns the session id stored in the context, if any.
func SessionIDFrom(ctx context.Context) string { return stream.SessionIDFrom(ctx) }

// UsageRecorder wraps an LLM client to record token usage and cost per session,
// and to abort calls once a session's budget is exceeded.
type UsageRecorder struct {
	Client   llmgw.Client
	Store    store.Store
	PriceIn  float64 // per 1M prompt tokens
	PriceOut float64 // per 1M completion tokens
	// ModelPrice, when set, overrides the global prices for a given model ref.
	ModelPrice func(model string) (in, out float64)
}

// Complete implements llmgw.Client.
func (u *UsageRecorder) Complete(ctx context.Context, req llmgw.Request) (llmgw.Response, error) {
	resp, err := u.Client.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, u.record(ctx, req, resp)
}

// Stream implements llmgw.StreamingClient by delegating to the wrapped client's
// stream (so role runs stream token deltas) and recording usage at the end.
func (u *UsageRecorder) Stream(ctx context.Context, req llmgw.Request, onDelta func(llmgw.Delta)) (llmgw.Response, error) {
	resp, err := llmgw.Stream(ctx, u.Client, req, onDelta)
	if err != nil {
		return resp, err
	}
	return resp, u.record(ctx, req, resp)
}

// record stores usage and enforces the session budget.
func (u *UsageRecorder) record(ctx context.Context, req llmgw.Request, resp llmgw.Response) error {
	sessionID := SessionIDFrom(ctx)
	if sessionID == "" || u.Store == nil {
		return nil
	}
	priceIn, priceOut := u.PriceIn, u.PriceOut
	if u.ModelPrice != nil {
		if in, out := u.ModelPrice(req.Model); in > 0 || out > 0 {
			priceIn, priceOut = in, out
		}
	}
	cost := float64(resp.Usage.PromptTokens)*priceIn/1e6 + float64(resp.Usage.CompletionTokens)*priceOut/1e6
	if err := u.Store.RecordUsage(ctx, domain.Usage{
		SessionID:        sessionID,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		CostUSD:          cost,
	}); err != nil {
		return err
	}
	sess, err := u.Store.GetSession(ctx, sessionID)
	if err != nil || sess.BudgetUSD <= 0 {
		return nil
	}
	total, err := u.Store.TotalUsage(ctx, sessionID)
	if err != nil {
		return nil
	}
	if total.CostUSD > sess.BudgetUSD {
		return fmt.Errorf("session: budget exceeded ($%.4f > $%.4f)", total.CostUSD, sess.BudgetUSD)
	}
	return nil
}
