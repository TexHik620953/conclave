package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
		client = t.env.safeHTTPClient()
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

// hostAllowed reports whether host matches the configured allowlist. An empty
// allowlist means "no explicit host restriction" (private IPs are still
// blocked for http_fetch).
func (e *Env) hostAllowed(host string) bool {
	for _, allowed := range e.AllowedHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

func (e *Env) checkHost(host string) error {
	if len(e.AllowedHosts) == 0 {
		return nil
	}
	if e.hostAllowed(host) {
		return nil
	}
	return fmt.Errorf("host %q is not in the network allowlist", host)
}

// safeHTTPClient returns a client that blocks requests to private, loopback and
// link-local addresses (unless the host is explicitly allowlisted) and
// re-validates every redirect, mitigating SSRF.
func (e *Env) safeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if !e.hostAllowed(host) {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if isBlockedIP(ip) {
						return nil, fmt.Errorf("refusing to connect to private address %s", ip)
					}
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http(s) scheme %q", req.URL.Scheme)
			}
			if err := e.checkHost(req.URL.Hostname()); err != nil {
				return err
			}
			return nil
		},
	}
}

// isBlockedIP reports whether ip is loopback, private, link-local, multicast or
// otherwise unsuitable for outbound agent fetches.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}
