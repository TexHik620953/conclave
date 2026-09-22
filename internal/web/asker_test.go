package web

import (
	"context"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/tool"
)

func TestBrokerAskAnswer(t *testing.T) {
	bus := event.NewBus()
	ch, cancel := bus.Subscribe(4)
	defer cancel()

	b := NewBroker(bus)
	ctx := tool.WithRunID(context.Background(), "run-1")
	done := make(chan tool.Answer, 1)
	go func() {
		ans, err := b.Ask(ctx, tool.Question{
			Question: "Which database?",
			Options:  []tool.Option{{Label: "Postgres"}, {Label: "SQLite"}},
		})
		if err == nil {
			done <- ans
		}
	}()

	var qid string
	select {
	case ev := <-ch:
		if ev.Type != event.UserQuestion {
			t.Fatalf("event type = %s", ev.Type)
		}
		if ev.RunID != "run-1" {
			t.Fatalf("run id = %q", ev.RunID)
		}
		qid, _ = ev.Data["id"].(string)
	case <-time.After(2 * time.Second):
		t.Fatal("no question event published")
	}
	if qid == "" {
		t.Fatal("question id missing")
	}
	if !b.Answer(qid, tool.Answer{Selected: []string{"Postgres"}}) {
		t.Fatal("answer was not accepted")
	}
	select {
	case ans := <-done:
		if len(ans.Selected) != 1 || ans.Selected[0] != "Postgres" {
			t.Fatalf("unexpected answer: %+v", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return")
	}
}

func TestBrokerAnswerUnknown(t *testing.T) {
	b := NewBroker(event.NewBus())
	if b.Answer("nope", tool.Answer{}) {
		t.Fatal("expected false for unknown question")
	}
}
