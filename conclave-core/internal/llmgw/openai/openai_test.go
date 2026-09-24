package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/llmgw"
)

func TestStreamParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		write("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New(srv.URL, "", 0)
	var deltas []string
	resp, err := c.Stream(context.Background(), llmgw.Request{Model: "m"}, func(d llmgw.Delta) {
		deltas = append(deltas, d.Content)
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Hello" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo" {
		t.Fatalf("deltas = %v", deltas)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if !strings.Contains(resp.Message.Content, "Hello") {
		t.Fatal("bad content")
	}
}
