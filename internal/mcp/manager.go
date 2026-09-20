package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/tool"
)

// transport is implemented by both stdio and HTTP MCP clients.
type transport interface {
	Name() string
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error)
	Close() error
}

// Manager owns connections to external MCP servers and exposes their tools.
type Manager struct {
	clients []transport
	tools   []tool.Tool
	errs    []string
}

// NewManager starts every enabled MCP server and collects their tools. Servers
// that fail to start are recorded in Errors and skipped.
func NewManager(ctx context.Context, servers map[string]config.MCPServer) *Manager {
	m := &Manager{}
	for name, srv := range servers {
		if srv.Enabled != nil && !*srv.Enabled {
			continue
		}
		client, err := startTransport(ctx, name, srv)
		if err != nil {
			m.errs = append(m.errs, err.Error())
			continue
		}
		m.clients = append(m.clients, client)
		defs, err := client.ListTools(ctx)
		if err != nil {
			m.errs = append(m.errs, fmt.Sprintf("mcp %s: list tools: %v", name, err))
			continue
		}
		for _, def := range defs {
			m.tools = append(m.tools, &mcpTool{client: client, server: name, def: def})
		}
	}
	return m
}

func startTransport(ctx context.Context, name string, srv config.MCPServer) (transport, error) {
	if srv.URL != "" {
		return newHTTPTransport(name, srv.URL, srv.Headers, 60*time.Second)
	}
	if len(srv.Command) == 0 {
		return nil, fmt.Errorf("mcp %s: configure either command or url", name)
	}
	return StartClient(ctx, name, srv.Command, srv.Env)
}

// Tools returns all discovered MCP tools.
func (m *Manager) Tools() []tool.Tool { return m.tools }

// Errors returns non-fatal startup errors.
func (m *Manager) Errors() []string { return m.errs }

// Close shuts down all clients.
func (m *Manager) Close() {
	for _, c := range m.clients {
		_ = c.Close()
	}
}

type mcpTool struct {
	client transport
	server string
	def    Tool
	mu     sync.Mutex
}

func (t *mcpTool) Name() string { return t.server + "__" + t.def.Name }
func (t *mcpTool) Description() string {
	return fmt.Sprintf("[MCP:%s] %s", t.server, t.def.Description)
}
func (t *mcpTool) Schema() json.RawMessage {
	if len(t.def.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.def.InputSchema
}

func (t *mcpTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	res, err := t.client.CallTool(ctx, t.def.Name, args)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	var out string
	for i, c := range res.Content {
		if i > 0 {
			out += "\n"
		}
		out += c.Text
	}
	return tool.Result{Content: out, IsError: res.IsError}, nil
}
