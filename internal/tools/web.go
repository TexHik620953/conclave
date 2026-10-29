package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/llm"
)

type webFetch struct{}

func (webFetch) Name() string { return "web_fetch" }
func (webFetch) Spec() llm.Tool {
	return llm.Tool{Name: "web_fetch", Description: "Fetch a URL and return readable text or raw HTML.",
		Parameters: schema(map[string]any{"url": prop("string", "HTTP or HTTPS URL")}, "url")}
}
func (webFetch) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	if env.Permissions.Network == "deny" {
		return "", fmt.Errorf("network access is disabled by permissions")
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return "", fmt.Errorf("only http(s) URLs are allowed")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "conclave/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2_000_000))
	if err != nil {
		return "", err
	}
	ct := resp.Header.Get("Content-Type")
	body := string(b)
	if strings.Contains(ct, "html") {
		body = stripHTML(body)
	}
	return clip(fmt.Sprintf("status: %d\ncontent-type: %s\n\n%s", resp.StatusCode, ct, body), 120_000), nil
}

func stripHTML(s string) string {
	repl := strings.NewReplacer(
		"<script", "\n<script", "</script>", "</script>\n",
		"<style", "\n<style", "</style>", "</style>\n",
		"<p", "\n<p", "<br", "\n<br", "<div", "\n<div", "<li", "\n<li",
	)
	s = repl.Replace(s)
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
