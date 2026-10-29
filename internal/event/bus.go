package event

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/texhik/conclave/internal/domain"
)

type Bus struct {
	mu     sync.RWMutex
	subs   map[int]chan domain.Event
	nextID int
	seq    atomic.Int64
	closed bool
}

func New() *Bus {
	return &Bus{subs: map[int]chan domain.Event{}}
}

func (b *Bus) Publish(ev domain.Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	ev.Seq = b.seq.Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// A slow renderer must never block an agent. State is also
			// persisted, and the UI can re-read it after a dropped delta.
		}
	}
}

func (b *Bus) Subscribe() (<-chan domain.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan domain.Event, 2048)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
