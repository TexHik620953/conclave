// Package stream carries assistant token deltas from role runs to a sink that
// persists/forwards them. Deltas are ephemeral: the canonical message is the
// finalized one emitted when Final is true.
package stream

import (
	"context"
	"encoding/json"

	"github.com/texhik/conclave/conclave-core/internal/domain"
)

// Envelope is the wire format published to client channels.
type Envelope struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Event     *domain.Event   `json:"event,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Delta is one streamed chunk of an assistant message.
type Delta struct {
	MessageID string
	Role      string
	Model     string
	NodeID    string
	Text      string
	Reasoning string
	// Final marks the end of the message.
	Final bool
}

// Sink consumes deltas for a session.
type Sink interface {
	Emit(ctx context.Context, sessionID string, d Delta)
}

type sessionIDKey struct{}

// WithSessionID attaches the session id to the context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFrom returns the session id stored in the context, if any.
func SessionIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

type ctxKey struct{}

type ctxState struct {
	sessionID string
	sink      Sink
}

// WithSink attaches a delta sink for a session to the context.
func WithSink(ctx context.Context, sessionID string, sink Sink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, ctxState{sessionID: sessionID, sink: sink})
}

// Emit forwards a delta to the sink attached to the context, if any.
func Emit(ctx context.Context, d Delta) {
	st, ok := ctx.Value(ctxKey{}).(ctxState)
	if !ok || st.sink == nil {
		return
	}
	st.sink.Emit(ctx, st.sessionID, d)
}

// DrainFunc returns and consumes the user messages queued since the last call.
type DrainFunc func(ctx context.Context) []string

type inboxKey struct{}

// WithInbox attaches an in-flight message drain to the context. Role runners
// call DrainInbox between model turns to inject user messages mid-run.
func WithInbox(ctx context.Context, drain DrainFunc) context.Context {
	if drain == nil {
		return ctx
	}
	return context.WithValue(ctx, inboxKey{}, drain)
}

// DrainInbox returns the user messages queued for the current run, if a drain
// is attached to the context.
func DrainInbox(ctx context.Context) []string {
	if f, ok := ctx.Value(inboxKey{}).(DrainFunc); ok && f != nil {
		return f(ctx)
	}
	return nil
}
