package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

type fakePublisher struct {
	mu      sync.Mutex
	payload [][]byte
}

func (f *fakePublisher) PublishDelta(_ string, payload []byte) error {
	f.mu.Lock()
	f.payload = append(f.payload, payload)
	f.mu.Unlock()
	return nil
}

func TestCoalescerPersistsAndFinalizes(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)
	if err := st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	pub := &fakePublisher{}
	c := NewCoalescer(st, log, pub, time.Hour)

	c.Emit(ctx, "s", stream.Delta{MessageID: "m1", Role: "senior", Text: "Hel"})
	// Force the periodic flush to persist the partial content.
	c.flushStale(ctx, time.Now().Add(time.Hour))
	msgs, _ := st.ListMessages(ctx, "s", 10)
	if len(msgs) != 1 || msgs[0].State != "streaming" || msgs[0].Content != "Hel" {
		t.Fatalf("partial message = %+v", msgs)
	}

	c.Emit(ctx, "s", stream.Delta{MessageID: "m1", Role: "senior", Text: "lo"})
	c.Emit(ctx, "s", stream.Delta{MessageID: "m1", Role: "senior", Text: "Hello", Final: true})

	msgs, _ = st.ListMessages(ctx, "s", 10)
	if len(msgs) != 1 || msgs[0].State != "done" || msgs[0].Content != "Hello" || msgs[0].Role != "senior" {
		t.Fatalf("final message = %+v", msgs)
	}

	evs, _ := log.List(ctx, "s", 0)
	found := false
	for _, e := range evs {
		if e.Type == domain.EventMessageCreated {
			found = true
		}
	}
	if !found {
		t.Fatal("message.created event not emitted")
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.payload) == 0 {
		t.Fatal("no delta published")
	}
	last := string(pub.payload[len(pub.payload)-1])
	if !strings.Contains(last, `"final":true`) || !strings.Contains(last, "Hello") {
		t.Fatalf("final delta = %s", last)
	}
}
