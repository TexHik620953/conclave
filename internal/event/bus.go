// Package event provides an in-process publish/subscribe bus for run progress.
package event

import (
	"sync"
	"time"
)

// Type identifies the kind of event.
type Type string

const (
	RunStarted   Type = "run.started"
	RunFinished  Type = "run.finished"
	NodeStarted  Type = "node.started"
	NodeFinished Type = "node.finished"
	RoleMessage  Type = "role.message"
	RoleDelta    Type = "role.delta"
	ToolCall     Type = "tool.call"
	ToolResult   Type = "tool.result"
	ContextUsage Type = "context.usage"
	Summarized   Type = "context.summarized"
	TodosUpdated Type = "todos.updated"
	UserQuestion Type = "user.question"
	Error        Type = "error"
)

// Event is a single progress notification.
type Event struct {
	Type    Type           `json:"type"`
	Time    time.Time      `json:"time"`
	RunID   string         `json:"run_id,omitempty"`
	NodeID  string         `json:"node_id,omitempty"`
	Role    string         `json:"role,omitempty"`
	Model   string         `json:"model,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// Bus fans events out to subscribers without blocking publishers.
type Bus struct {
	mu   sync.RWMutex
	subs map[int]chan Event
	next int
}

// NewBus creates an empty bus.
func NewBus() *Bus {
	return &Bus{subs: map[int]chan Event{}}
}

// Subscribe returns a channel of events and a cancel function.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish delivers an event to all subscribers, dropping when a subscriber is
// saturated.
func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
