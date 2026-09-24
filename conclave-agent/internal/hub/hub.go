// Package hub is a tiny in-process fanout for forwarding core events to the
// local Web UI over SSE.
package hub

import "sync"

// Hub fans raw envelopes out to UI subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[int]chan []byte
	next int
}

// New creates an empty hub.
func New() *Hub { return &Hub{subs: map[int]chan []byte{}} }

// Subscribe returns a channel of envelopes and a cancel function.
func (h *Hub) Subscribe(buffer int) (<-chan []byte, func()) {
	if buffer <= 0 {
		buffer = 1024
	}
	ch := make(chan []byte, buffer)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

// Publish delivers an envelope to all subscribers, dropping on saturation.
func (h *Hub) Publish(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- data:
		default:
		}
	}
}
