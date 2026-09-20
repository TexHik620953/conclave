package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type webFetch struct{ env *Env }

func (t *webFetch) Name() string { return "http_fetch" }
func (t *webFetch) Description() string {
	return "Fetch a URL over HTTP(S) and return the response body. Hosts are restricted by policy."
}
func (t *webFetch) Schema() json.RawMessage {
	return schema(map[string]any{
		"url":     map[string]any{"type": "string"},
		"method":  map[string]any{"type": "string", "description": "HTTP method, defaults to GET."},
		"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"body":    map[string]any{"type": "string"},
	}, "url")
}

func (t *webFetch) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.Network {
		return Result{Content: "network permission denied", IsError: true}, nil
	}
	var in struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	u, err := url.Parse(in.URL)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{Content: "only http/https URLs are allowed", IsError: true}, nil
	}
	if err := t.env.checkHost(u.Hostname()); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	method := strings.ToUpper(in.Method)
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if in.Body != "" {
		bodyReader = strings.NewReader(in.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, in.URL, bodyReader)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	client := t.env.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d %s\n\n", resp.StatusCode, resp.Header.Get("Content-Type"))
	b.Write(data)
	return Result{Content: truncate(t.env, b.String()), IsError: resp.StatusCode >= 400}, nil
}

func (e *Env) checkHost(host string) error {
	if len(e.AllowedHosts) == 0 {
		return nil
	}
	for _, allowed := range e.AllowedHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not in the network allowlist", host)
}
