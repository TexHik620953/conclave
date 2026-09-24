// Package events provides an append-only event log with in-process fanout.
package events

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Log appends events to a store and fans them out to subscribers.
type Log struct {
	store store.Store

	mu   sync.RWMutex
	subs map[int]chan domain.Event
	next int
}

// New creates an event log over a store.
func New(st store.Store) *Log {
	return &Log{store: st, subs: map[int]chan domain.Event{}}
}

// Append assigns an id/seq and persists an event, then publishes it.
func (l *Log) Append(ctx context.Context, e domain.Event) (domain.Event, error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	seq, err := l.store.AppendEvent(ctx, e)
	if err != nil {
		return domain.Event{}, err
	}
	e.Seq = seq
	l.publish(e)
	return e, nil
}

// List returns events for a session with seq greater than fromSeq.
func (l *Log) List(ctx context.Context, sessionID string, fromSeq int64) ([]domain.Event, error) {
	return l.store.ListEvents(ctx, sessionID, fromSeq)
}

// Subscribe returns a channel of events and a cancel function. Slow
// subscribers have events dropped rather than blocking publishers.
func (l *Log) Subscribe(buffer int) (<-chan domain.Event, func()) {
	if buffer <= 0 {
		buffer = 1024
	}
	ch := make(chan domain.Event, buffer)
	l.mu.Lock()
	id := l.next
	l.next++
	l.subs[id] = ch
	l.mu.Unlock()
	cancel := func() {
		l.mu.Lock()
		if c, ok := l.subs[id]; ok {
			delete(l.subs, id)
			close(c)
		}
		l.mu.Unlock()
	}
	return ch, cancel
}

func (l *Log) publish(e domain.Event) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, ch := range l.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
