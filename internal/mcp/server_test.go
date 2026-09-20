package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
)

func TestServerElicitation(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()

	srv := &Server{Name: "test", Version: "0", In: c2sR, Out: s2cW}
	srv.AddTool(ToolSpec{
		Name:        "ask",
		Description: "ask",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (*CallToolResult, error) {
			res, err := srv.Elicit(ctx, "Pick one", json.RawMessage(`{"type":"object","properties":{"selected":{"type":"string"}}}`))
			if err != nil {
				return ErrorText(err.Error()), nil
			}
			return Text("action=" + res.Action + " selected=" + toString(res.Content["selected"])), nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	clientEnc := json.NewEncoder(c2sW)
	clientDec := json.NewDecoder(bufio.NewReader(s2cR))

	// initialize
	if err := clientEnc.Encode(Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize"}); err != nil {
		t.Fatal(err)
	}
	var initResp Response
	if err := clientDec.Decode(&initResp); err != nil {
		t.Fatal(err)
	}

	// tools/call
	params, _ := json.Marshal(CallToolParams{Name: "ask", Arguments: json.RawMessage("{}")})
	if err := clientEnc.Encode(Request{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call", Params: params}); err != nil {
		t.Fatal(err)
	}

	// expect elicitation/create request
	var elicitReq Request
	if err := clientDec.Decode(&elicitReq); err != nil {
		t.Fatal(err)
	}
	if elicitReq.Method != "elicitation/create" {
		t.Fatalf("method = %q, want elicitation/create", elicitReq.Method)
	}
	if err := clientEnc.Encode(Response{
		JSONRPC: "2.0",
		ID:      elicitReq.ID,
		Result:  map[string]any{"action": "accept", "content": map[string]any{"selected": "A"}},
	}); err != nil {
		t.Fatal(err)
	}

	// read tools/call response
	var callResp Response
	if err := clientDec.Decode(&callResp); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(callResp.Result)
	var out CallToolResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Content) == 0 || out.Content[0].Text != "action=accept selected=A" {
		t.Fatalf("unexpected result: %+v", out.Content)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
