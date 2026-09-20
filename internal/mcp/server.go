package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// ToolSpec binds an MCP tool declaration to its handler.
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Handler     func(ctx context.Context, args json.RawMessage) (*CallToolResult, error)
}

// ElicitResult is the client's response to an elicitation/create request.
type ElicitResult struct {
	Action  string         `json:"action"` // accept | decline | cancel
	Content map[string]any `json:"content"`
}

// Server is an MCP server speaking newline-delimited JSON-RPC over a stream.
// It supports both serving tools and issuing requests to the client
// (elicitation).
type Server struct {
	Name    string
	Version string
	In      io.Reader
	Out     io.Writer

	mu     sync.Mutex
	tools  []ToolSpec
	writeM sync.Mutex
	enc    *json.Encoder

	reqMu   sync.Mutex
	pending map[int]chan Response
	nextID  int
}

// AddTool registers a tool.
func (s *Server) AddTool(spec ToolSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = append(s.tools, spec)
}

// Serve processes requests until the input stream ends or ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	s.enc = json.NewEncoder(s.Out)
	s.pending = map[int]chan Response{}
	s.mu.Unlock()

	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var wg sync.WaitGroup
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = s.write(Response{JSONRPC: "2.0", Error: &RPCError{Code: CodeParseError, Message: err.Error()}})
			continue
		}
		// Response to a server-initiated request (elicitation).
		if req.Method == "" && len(req.ID) > 0 {
			s.dispatch(line)
			continue
		}
		if len(req.ID) == 0 {
			continue // notification
		}
		wg.Add(1)
		go func(req Request) {
			defer wg.Done()
			_ = s.write(s.handle(ctx, req))
		}(req)
	}
	wg.Wait()
	return scanner.Err()
}

func (s *Server) write(v any) error {
	s.writeM.Lock()
	defer s.writeM.Unlock()
	return s.enc.Encode(v)
}

func (s *Server) dispatch(raw []byte) {
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return
	}
	var id int
	if err := json.Unmarshal(resp.ID, &id); err != nil {
		return
	}
	s.reqMu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.reqMu.Unlock()
	if ok {
		ch <- resp
	}
}

// Elicit sends an elicitation/create request to the client and waits for a
// response. It returns an error when the client does not support elicitation.
func (s *Server) Elicit(ctx context.Context, message string, schema json.RawMessage) (*ElicitResult, error) {
	s.reqMu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan Response, 1)
	if s.pending == nil {
		s.pending = map[int]chan Response{}
	}
	s.pending[id] = ch
	s.reqMu.Unlock()

	idRaw, _ := json.Marshal(id)
	params, _ := json.Marshal(map[string]any{
		"message":         message,
		"requestedSchema": schema,
	})
	if err := s.write(Request{JSONRPC: "2.0", ID: idRaw, Method: "elicitation/create", Params: params}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("elicitation connection closed")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		raw, _ := json.Marshal(resp.Result)
		var out ElicitResult
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
}

func (s *Server) handle(ctx context.Context, req Request) Response {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities:    ServerCapabilities{Tools: &struct{}{}},
			ServerInfo:      Implementation{Name: s.Name, Version: s.Version},
		}
	case "ping":
		resp.Result = struct{}{}
	case "tools/list":
		resp.Result = s.listTools()
	case "tools/call":
		res, err := s.callTool(ctx, req.Params)
		if err != nil {
			resp.Error = &RPCError{Code: CodeInternalError, Message: err.Error()}
		} else {
			resp.Result = res
		}
	default:
		resp.Error = &RPCError{Code: CodeMethodNotFound, Message: fmt.Sprintf("method %q not found", req.Method)}
	}
	return resp
}

func (s *Server) listTools() ListToolsResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return ListToolsResult{Tools: out}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (*CallToolResult, error) {
	var params CallToolParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	var spec *ToolSpec
	for i := range s.tools {
		if s.tools[i].Name == params.Name {
			spec = &s.tools[i]
			break
		}
	}
	s.mu.Unlock()
	if spec == nil {
		return ErrorText(fmt.Sprintf("unknown tool %q", params.Name)), nil
	}
	if spec.Handler == nil {
		return ErrorText(fmt.Sprintf("tool %q has no handler", params.Name)), nil
	}
	res, err := spec.Handler(ctx, params.Arguments)
	if err != nil {
		return ErrorText(err.Error()), nil
	}
	if res == nil {
		return Text("(no result)"), nil
	}
	return res, nil
}
