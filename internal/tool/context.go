package tool

import "context"

type ctxKey int

const runIDKey ctxKey = iota

// WithRunID attaches a run identifier to a context so Askers can correlate
// questions with the run they belong to.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey, runID)
}

// RunIDFrom returns the run identifier attached to the context, if any.
func RunIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey).(string); ok {
		return v
	}
	return ""
}
