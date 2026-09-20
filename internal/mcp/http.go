package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpTransport speaks MCP over Streamable HTTP (POST + optional SSE).
type httpTransport struct {
	name      string
	url       string
	headers   map[string]string
	sessionID string
	client    *http.Client
	mu        sync.Mutex
	nextID    int
}

func newHTTPTransport(name, url string, headers map[string]string, timeout time.Duration) (*httpTransport, error) {
	if url == "" {
		return nil, fmt.Errorf("mcp %s: empty url", name)
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	h := &httpTransport{
		name:    name,
		url:     url,
		headers: headers,
		client:  &http.Client{Timeout: timeout},
	}
	if err := h.initialize(context.Background()); err != nil {
		return nil, err
	}
	return h, nil
}

// Name returns the server name.
func (h *httpTransport) Name() string { return h.name }

func (h *httpTransport) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      Implementation{Name: "conclave", Version: "0.1.0"},
	}
	var res InitializeResult
	if err := h.call(ctx, "initialize", params, &res); err != nil {
		return err
	}
	_ = h.notify(ctx, "notifications/initialized", nil)
	return nil
}

func (h *httpTransport) notify(ctx context.Context, method string, params any) error {
	req := Request{JSONRPC: "2.0", Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	h.setHeaders(httpReq)
	resp, err := h.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

func (h *httpTransport) call(ctx context.Context, method string, params any, result any) error {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	h.mu.Unlock()

	idRaw, _ := json.Marshal(id)
	req := Request{JSONRPC: "2.0", ID: idRaw, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	h.setHeaders(httpReq)
	resp, err := h.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mcp %s: %w", h.name, err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.sessionID = sid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("mcp %s: HTTP %d: %s", h.name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return h.parseSSE(resp.Body, id, result)
	}
	return h.decode(resp.Body, result)
}

func (h *httpTransport) parseSSE(r io.Reader, id int, result any) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var rpc Response
		if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
			continue
		}
		var respID int
		if err := json.Unmarshal(rpc.ID, &respID); err != nil || respID != id {
			continue
		}
		return applyResponse(&rpc, result)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("mcp %s: no response for request %d", h.name, id)
}

func (h *httpTransport) decode(r io.Reader, result any) error {
	raw, err := io.ReadAll(io.LimitReader(r, 16*1024*1024))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var rpc Response
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return fmt.Errorf("mcp %s: decoding response: %w", h.name, err)
	}
	return applyResponse(&rpc, result)
}

func applyResponse(rpc *Response, result any) error {
	if rpc.Error != nil {
		return fmt.Errorf("mcp: %s", rpc.Error.Message)
	}
	if result != nil && rpc.Result != nil {
		raw, err := json.Marshal(rpc.Result)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, result)
	}
	return nil
}

func (h *httpTransport) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
}

// ListTools fetches the server's tool list.
func (h *httpTransport) ListTools(ctx context.Context) ([]Tool, error) {
	var res ListToolsResult
	if err := h.call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes a tool by name.
func (h *httpTransport) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var res CallToolResult
	if err := h.call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Close releases the session.
func (h *httpTransport) Close() error {
	if h.sessionID == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, h.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", h.sessionID)
	resp, err := h.client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return nil
}
