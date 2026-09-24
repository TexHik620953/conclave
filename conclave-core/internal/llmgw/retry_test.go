package llmgw

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flaky struct {
	n    int
	fail int
}

func (f *flaky) Complete(context.Context, Request) (Response, error) {
	f.n++
	if f.n <= f.fail {
		return Response{}, errors.New("transient")
	}
	return Response{Message: Message{Role: "assistant", Content: "ok"}}, nil
}

func TestRetrySucceedsAfterTransientErrors(t *testing.T) {
	client := &flaky{fail: 2}
	r := Retry{Client: client, Attempts: 3, BaseDelay: time.Millisecond}
	resp, err := r.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.Content != "ok" || client.n != 3 {
		t.Fatalf("resp=%+v calls=%d", resp, client.n)
	}
}

func TestRetryExhausts(t *testing.T) {
	client := &flaky{fail: 5}
	r := Retry{Client: client, Attempts: 2, BaseDelay: time.Millisecond}
	if _, err := r.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if client.n != 2 {
		t.Fatalf("calls = %d, want 2", client.n)
	}
}
