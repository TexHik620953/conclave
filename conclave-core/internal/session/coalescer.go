package session

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

// DeltaPublisher publishes ephemeral token deltas to a session's delta channel.
type DeltaPublisher interface {
	PublishDelta(sessionID string, payload []byte) error
}

// Coalescer implements stream.Sink: it buffers token deltas per message and
// periodically persists the partial content and publishes it, then finalizes
// the message. This bounds DB writes and publication rate.
type Coalescer struct {
	Store    store.Store
	Log      *events.Log
	Pub      DeltaPublisher
	Interval time.Duration

	mu      sync.Mutex
	buffers map[string]*deltaBuf
}

type deltaBuf struct {
	sessionID string
	role      string
	model     string
	nodeID    string
	content   strings.Builder
	reasoning strings.Builder
	lastFlush time.Time
}

type deltaSnapshot struct {
	sessionID string
	role      string
	model     string
	nodeID    string
	content   string
	reasoning string
}

type deltaPayload struct {
	MessageID string `json:"message_id"`
	Role      string `json:"role,omitempty"`
	Model     string `json:"model,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	Final     bool   `json:"final,omitempty"`
}

// NewCoalescer creates a coalescer. interval defaults to 75ms.
func NewCoalescer(st store.Store, log *events.Log, pub DeltaPublisher, interval time.Duration) *Coalescer {
	if interval <= 0 {
		interval = 75 * time.Millisecond
	}
	return &Coalescer{Store: st, Log: log, Pub: pub, Interval: interval, buffers: map[string]*deltaBuf{}}
}

// Emit implements stream.Sink.
func (c *Coalescer) Emit(ctx context.Context, sessionID string, d stream.Delta) {
	if d.MessageID == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	b, ok := c.buffers[d.MessageID]
	if !ok {
		b = &deltaBuf{sessionID: sessionID, role: d.Role, model: d.Model, nodeID: d.NodeID, lastFlush: now}
		c.buffers[d.MessageID] = b
	}
	if d.Final && d.Text != "" {
		b.content.Reset()
	}
	b.content.WriteString(d.Text)
	b.reasoning.WriteString(d.Reasoning)
	flush := d.Final || now.Sub(b.lastFlush) >= c.Interval
	snap := deltaSnapshot{
		sessionID: b.sessionID, role: b.role, model: b.model, nodeID: b.nodeID,
		content: b.content.String(), reasoning: b.reasoning.String(),
	}
	if flush {
		b.lastFlush = now
		if d.Final {
			delete(c.buffers, d.MessageID)
		}
	}
	c.mu.Unlock()
	if flush {
		c.flush(ctx, d.MessageID, snap, d.Final)
	}
}

// Run periodically flushes buffered deltas so partial content is persisted even
// between sparse updates.
func (c *Coalescer) Run(ctx context.Context) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.flushStale(ctx, now)
		}
	}
}

func (c *Coalescer) flushStale(ctx context.Context, now time.Time) {
	type item struct {
		id   string
		snap deltaSnapshot
	}
	var pending []item
	c.mu.Lock()
	for id, b := range c.buffers {
		if now.Sub(b.lastFlush) < c.Interval {
			continue
		}
		b.lastFlush = now
		pending = append(pending, item{id: id, snap: deltaSnapshot{
			sessionID: b.sessionID, role: b.role, model: b.model, nodeID: b.nodeID,
			content: b.content.String(), reasoning: b.reasoning.String(),
		}})
	}
	c.mu.Unlock()
	for _, it := range pending {
		c.flush(ctx, it.id, it.snap, false)
	}
}

func (c *Coalescer) flush(ctx context.Context, id string, s deltaSnapshot, final bool) {
	if c.Store != nil {
		if final {
			_ = c.Store.FinalizeMessage(ctx, id, s.content)
		} else {
			_ = c.Store.UpsertMessage(ctx, domain.Message{
				ID: id, SessionID: s.sessionID, NodeID: s.nodeID, Role: s.role, Model: s.model,
				Kind: "assistant", Content: s.content, State: "streaming",
			})
		}
	}
	if c.Pub != nil {
		data, _ := json.Marshal(deltaPayload{
			MessageID: id, Role: s.role, Model: s.model, NodeID: s.nodeID,
			Text: s.content, Reasoning: s.reasoning, Final: final,
		})
		env, _ := json.Marshal(stream.Envelope{Type: "message.delta", SessionID: s.sessionID, Data: data})
		_ = c.Pub.PublishDelta(s.sessionID, env)
	}
	if final && c.Log != nil {
		_, _ = c.Log.Append(ctx, domain.Event{
			SessionID: s.sessionID, Type: domain.EventMessageCreated,
			Payload: map[string]any{"message_id": id, "content": s.content, "role": s.role, "model": s.model},
		})
	}
}
