package hub

import (
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	h := New()
	ch, cancel := h.Subscribe(4)
	defer cancel()
	h.Publish([]byte(`{"n":1}`))
	select {
	case d := <-ch:
		if string(d) != `{"n":1}` {
			t.Fatalf("got %s", d)
		}
	case <-time.After(time.Second):
		t.Fatal("no message")
	}
}

func TestPublishDropsWhenFull(t *testing.T) {
	h := New()
	_, cancel := h.Subscribe(1)
	defer cancel()
	// Should not block even when the buffer is full.
	for i := 0; i < 10; i++ {
		h.Publish([]byte(`x`))
	}
}
