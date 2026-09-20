package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Client is a stdio MCP client.
type Client struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex
	nextID  int
	pending map[int]chan Response
	closed  chan struct{}
	err     error
}

// StartClient launches an MCP server subprocess and performs the handshake.
func StartClient(ctx context.Context, name string, command []string, env map[string]string) (*Client, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("mcp %s: empty command", name)
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	c := &Client{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20),
		pending: map[int]chan Response{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Name returns the server name.
func (c *Client) Name() string { return c.name }

func (c *Client) readLoop() {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var resp Response
			if jsonErr := json.Unmarshal(line, &resp); jsonErr == nil {
				c.dispatch(resp)
			}
		}
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			close(c.closed)
			return
		}
	}
}

func (c *Client) dispatch(resp Response) {
	var id int
	if err := json.Unmarshal(resp.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	idRaw, _ := json.Marshal(id)
	req := Request{JSONRPC: "2.0", ID: idRaw, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	_, err = c.stdin.Write(append(data, '\n'))
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("mcp %s: write: %w", c.name, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("mcp %s: connection closed", c.name)
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp %s: %s", c.name, resp.Error.Message)
		}
		if result != nil && resp.Result != nil {
			raw, err := json.Marshal(resp.Result)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return fmt.Errorf("mcp %s: connection closed", c.name)
	}
}

func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      Implementation{Name: "conclave", Version: "0.1.0"},
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var res InitializeResult
	if err := c.call(callCtx, "initialize", params, &res); err != nil {
		return err
	}
	notif, _ := json.Marshal(Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	c.mu.Lock()
	_, _ = c.stdin.Write(append(notif, '\n'))
	c.mu.Unlock()
	return nil
}

// ListTools fetches the server's tool list.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var res ListToolsResult
	if err := c.call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes a tool by name.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var res CallToolResult
	if err := c.call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Close terminates the server process.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.mu.Unlock()
	select {
	case <-c.closed:
	default:
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
	return nil
}
