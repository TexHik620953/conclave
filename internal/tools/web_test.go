package tools

import (
	"context"
	"strings"
	"testing"
)

func TestHTMLToMarkdown(t *testing.T) {
	html := `<html><head><title>x</title><script>bad()</script></head><body><h1>Title</h1><p>Hello <a href="https://example.com">world</a>.</p><ul><li>One</li><li>Two</li></ul></body></html>`
	out := htmlToMarkdown(html)
	for _, want := range []string{"# Title", "Hello world (https://example.com)", "- One", "- Two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bad()") {
		t.Fatal("script content was not removed")
	}
}

func TestSafeDialBlocksLoopback(t *testing.T) {
	_, err := safeDialContext(false)(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Fatalf("expected private-address rejection, got %v", err)
	}
}
