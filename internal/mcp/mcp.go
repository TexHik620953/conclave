package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
)

type Manager struct {
	clients []*Client
	tools   []*Tool
}

type Client struct {
	Name    string
	tr      transport
	nextID  int
	mu      sync.Mutex
	pending map[int]chan rpcResponse
	tools   []mcpTool
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type transport interface {
	Request(ctx context.Context, req rpcRequest) (json.RawMessage, error)
	Notify(ctx context.Context, req rpcRequest) error
	Close() error
}

func Connect(ctx context.Context, servers map[string]config.MCPServer) *Manager {
	m := &Manager{}
	for name, cfg := range servers {
		if cfg.Enabled != nil && !*cfg.Enabled {
			continue
		}
		c, err := connectOne(ctx, name, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp %s: %v\n", name, err)
			continue
		}
		m.clients = append(m.clients, c)
		for _, mt := range c.tools {
			m.tools = append(m.tools, &Tool{client: c, spec: mt, name: "mcp__" + sanitize(name) + "__" + sanitize(mt.Name)})
		}
	}
	return m
}

func connectOne(ctx context.Context, name string, cfg config.MCPServer) (*Client, error) {
	var tr transport
	switch cfg.Type {
	case "", "local", "stdio":
		if len(cfg.Command) == 0 {
			return nil, errors.New("local MCP server has no command")
		}
		t, err := newStdioTransport(cfg.Command, cfg.Env)
		if err != nil {
			return nil, err
		}
		tr = t
	case "remote", "http", "sse":
		if cfg.URL == "" {
			return nil, errors.New("remote MCP server has no url")
		}
		tr = &httpTransport{url: cfg.URL, headers: cfg.Headers, http: &http.Client{Timeout: 60 * time.Second}}
	default:
		return nil, fmt.Errorf("unknown MCP type %q", cfg.Type)
	}
	c := &Client{Name: name, tr: tr, pending: map[int]chan rpcResponse{}}
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := c.request(initCtx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "conclave", "version": "0.1"},
	}); err != nil {
		tr.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	_ = c.notify(initCtx, "notifications/initialized", map[string]any{})
	raw, err := c.request(initCtx, "tools/list", map[string]any{})
	if err != nil {
		tr.Close()
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	var listed struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		tr.Close()
		return nil, err
	}
	c.tools = listed.Tools
	return c, nil
}

func (m *Manager) Close() {
	for _, c := range m.clients {
		_ = c.tr.Close()
	}
}

func (m *Manager) Tools() []tools.Tool {
	out := make([]tools.Tool, 0, len(m.tools))
	for _, t := range m.tools {
		out = append(out, t)
	}
	return out
}

func (m *Manager) AddTo(r *tools.Registry) {
	for _, t := range m.tools {
		r.Add(t)
	}
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	return c.tr.Request(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.tr.Notify(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

type Tool struct {
	client *Client
	spec   mcpTool
	name   string
}

func (t *Tool) Name() string { return t.name }
func (t *Tool) Spec() llm.Tool {
	schema := t.spec.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return llm.Tool{Name: t.name, Description: t.spec.Description, Parameters: schema}
}
func (t *Tool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in any = map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", err
		}
	}
	raw, err := t.client.request(ctx, "tools/call", map[string]any{"name": t.spec.Name, "arguments": in})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return string(raw), nil
	}
	var b strings.Builder
	for _, c := range res.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		} else if c.Type != "" {
			fmt.Fprintf(&b, "[%s content omitted]", c.Type)
		}
	}
	if res.IsError {
		return b.String(), fmt.Errorf("MCP tool returned an error")
	}
	if b.Len() == 0 {
		return string(raw), nil
	}
	return b.String(), nil
}

type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	encMu   sync.Mutex
	pending map[int]chan rpcResponse
	mu      sync.Mutex
	closed  bool
}

func newStdioTransport(argv []string, extraEnv map[string]string) (*stdioTransport, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, pending: map[int]chan rpcResponse{}}
	go t.readLoop(stdout)
	return t, nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		var id int
		if err := json.Unmarshal(resp.ID, &id); err != nil {
			continue
		}
		t.mu.Lock()
		ch := t.pending[id]
		delete(t.pending, id)
		t.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		for _, ch := range t.pending {
			close(ch)
		}
		t.pending = map[int]chan rpcResponse{}
	}
	t.mu.Unlock()
}

func (t *stdioTransport) Request(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	ch := make(chan rpcResponse, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("MCP server is closed")
	}
	t.pending[req.ID] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, req.ID)
		t.mu.Unlock()
	}()
	t.encMu.Lock()
	err := json.NewEncoder(t.stdin).Encode(req)
	t.encMu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("MCP server exited")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (t *stdioTransport) Notify(ctx context.Context, req rpcRequest) error {
	t.encMu.Lock()
	defer t.encMu.Unlock()
	return json.NewEncoder(t.stdin).Encode(req)
}

func (t *stdioTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_, _ = t.cmd.Process.Wait()
	return nil
}

type httpTransport struct {
	url       string
	headers   map[string]string
	http      *http.Client
	sessionID string
}

func (t *httpTransport) Request(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	b, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := t.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("MCP HTTP %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEResponse(resp.Body)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

func (t *httpTransport) Notify(ctx context.Context, req rpcRequest) error {
	_, err := t.Request(ctx, req)
	return err
}

func (t *httpTransport) Close() error { return nil }

func readSSEResponse(r io.Reader) (json.RawMessage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var resp rpcResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if len(resp.Result) > 0 {
			return resp.Result, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("MCP SSE stream ended without a result")
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
