package web

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/tool"
)

// Broker turns ask_user calls into WebSocket questions and waits for answers
// submitted through the HTTP API.
type Broker struct {
	bus *event.Bus
	// Emit, when set, routes events through the engine's persistence path.
	Emit func(event.Event)

	mu      sync.Mutex
	pending map[string]pendingQuestion
	seq     int
}

type pendingQuestion struct {
	runID string
	ch    chan tool.Answer
}

// NewBroker creates a question broker over the event bus.
func NewBroker(bus *event.Bus) *Broker {
	return &Broker{bus: bus, pending: map[string]pendingQuestion{}}
}

// Ask publishes a question and blocks until the user answers or the context is
// cancelled. Without an answer it reports ErrNoInteractiveUser so callers can
// fall back to autonomous behaviour.
func (b *Broker) Ask(ctx context.Context, q tool.Question) (tool.Answer, error) {
	runID := tool.RunIDFrom(ctx)
	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("q%d", b.seq)
	ch := make(chan tool.Answer, 1)
	b.pending[id] = pendingQuestion{runID: runID, ch: ch}
	b.mu.Unlock()

	ev := event.Event{
		Type:    event.UserQuestion,
		RunID:   runID,
		Message: q.Question,
		Data: map[string]any{
			"id":           id,
			"header":       q.Header,
			"question":     q.Question,
			"options":      q.Options,
			"multiple":     q.Multiple,
			"allow_custom": q.AllowCustom,
		},
	}
	if b.Emit != nil {
		b.Emit(ev)
	} else {
		b.bus.Publish(ev)
	}

	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	select {
	case ans := <-ch:
		return ans, nil
	case <-ctx.Done():
		b.forget(id)
		return tool.Answer{}, tool.ErrNoInteractiveUser
	case <-timer.C:
		b.forget(id)
		return tool.Answer{}, tool.ErrNoInteractiveUser
	}
}

func (b *Broker) forget(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// Answer delivers an answer to a pending question.
func (b *Broker) Answer(id string, ans tool.Answer) bool {
	return b.AnswerFor("", id, ans)
}

// AnswerFor delivers an answer, optionally requiring the question to belong to
// the given run.
func (b *Broker) AnswerFor(runID, id string, ans tool.Answer) bool {
	b.mu.Lock()
	p, ok := b.pending[id]
	if ok && runID != "" && p.runID != "" && p.runID != runID {
		ok = false
	}
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok {
		p.ch <- ans
	}
	return ok
}
