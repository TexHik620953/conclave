package llm

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
	"sort"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
)

type OpenAI struct {
	baseURL           string
	apiKey            string
	headers           map[string]string
	http              *http.Client
	firstTokenTimeout time.Duration
	idleTimeout       time.Duration
}

var errProviderTimeout = errors.New("provider timeout")

func NewOpenAI(p config.Provider) *OpenAI {
	return NewOpenAIWithTimeouts(p, 0, 0)
}

func NewOpenAIWithTimeouts(p config.Provider, firstToken, idle time.Duration) *OpenAI {
	if firstToken <= 0 {
		firstToken = durationEnv("CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT", 180*time.Second)
	}
	if idle <= 0 {
		idle = durationEnv("CONCLAVE_LLM_IDLE_TIMEOUT", 180*time.Second)
	}
	return &OpenAI{
		baseURL: strings.TrimRight(p.BaseURL, "/"),
		apiKey:  p.APIKey,
		headers: p.Headers,
		http: &http.Client{
			Transport: &http.Transport{ResponseHeaderTimeout: firstToken + 30*time.Second},
		},
		firstTokenTimeout: firstToken,
		idleTimeout:       idle,
	}
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

type chatMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type apiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function apiToolFunction `json:"function"`
}

type apiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type apiTool struct {
	Type     string          `json:"type"`
	Function apiToolFunction `json:"function"`
}

type apiRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	Tools         []apiTool     `json:"tools,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions *streamOpts   `json:"stream_options,omitempty"`
	Temperature   *float64      `json:"temperature,omitempty"`
	MaxTokens     int           `json:"max_tokens,omitempty"`
	Reasoning     string        `json:"reasoning_effort,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (o *OpenAI) Stream(ctx context.Context, model Model, req Request, sink func(Event) error) error {
	body, err := o.buildRequest(model, req)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		emitted := false
		err := o.streamOnce(ctx, body, model, func(e Event) error {
			emitted = true
			return sink(e)
		})
		if err == nil {
			return nil
		}
		lastErr = err
		if emitted || !retryable(err) {
			return err
		}
		// A stalled provider gets one fresh attempt, not three full timeouts.
		if errors.Is(err, errProviderTimeout) && attempt >= 1 {
			return err
		}
	}
	return lastErr
}

type httpError struct {
	Status int
	Body   string
}

func (e httpError) Error() string {
	return fmt.Sprintf("provider HTTP %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func retryable(err error) bool {
	if errors.Is(err, errProviderTimeout) {
		return true
	}
	he, ok := err.(httpError)
	if !ok {
		return false
	}
	return he.Status == 408 || he.Status == 409 || he.Status == 429 || he.Status >= 500
}

func (o *OpenAI) buildRequest(model Model, req Request) ([]byte, error) {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		cm := chatMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, apiToolCall{
				ID: tc.ID, Type: "function",
				Function: apiToolFunction{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		msgs = append(msgs, cm)
	}
	out := apiRequest{
		Model:         model.ID,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOpts{IncludeUsage: true},
		MaxTokens:     req.MaxTokens,
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = model.MaxOutput
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = 8192
	}
	// Some provider model lists advertise enormous completion limits. Sending
	// that value makes the provider reserve the whole context window for output.
	const maxSafeOutput = 32768
	if out.MaxTokens > maxSafeOutput {
		out.MaxTokens = maxSafeOutput
	}
	if model.Context > 0 {
		estimatedInput := estimateTokens(msgs, req.Tools)
		remaining := model.Context - estimatedInput - 512
		if remaining < 512 {
			return nil, fmt.Errorf("context window exceeded: estimated input %d leaves no room for output in %d tokens; compact the conversation", estimatedInput, model.Context)
		}
		if out.MaxTokens > remaining {
			out.MaxTokens = remaining
		}
	}
	if req.Temperature > 0 {
		t := req.Temperature
		out.Temperature = &t
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, apiTool{
			Type: "function",
			Function: apiToolFunction{
				Name: t.Name, Arguments: mustJSON(t.Parameters),
			},
		})
	}
	// The API expects function.parameters as an object, not a string. The
	// custom marshaling below fixes this up without a second struct.
	if req.Effort != "" && (model.Capabilities.Effort || model.Capabilities.Reasoning) {
		effort := req.Effort
		if model.EffortMap != nil {
			if v, ok := model.EffortMap[effort]; ok {
				effort = v
			}
		}
		if effort == "ultra" {
			effort = "high"
		}
		out.Reasoning = effort
	}
	return json.Marshal(out)
}

// mustJSON is used for tool parameters. OpenAI's wire format differs from
// Tool.Parameters, so the request is post-processed in marshalRequest.
func estimateTokens(msgs []chatMessage, toolSpecs []Tool) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content) + len(m.Name) + len(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	for _, t := range toolSpecs {
		chars += len(t.Name) + len(t.Description)
		b, _ := json.Marshal(t.Parameters)
		chars += len(b)
	}
	// Conservative estimate for mixed English/Russian/code content.
	return chars/3 + len(msgs)*4 + 32
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (o *OpenAI) streamOnce(ctx context.Context, body []byte, model Model, sink func(Event) error) error {
	// Replace function.arguments strings for tools with function.parameters
	// objects. Doing it on a generic map keeps the message structs readable.
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return err
	}
	if tools, ok := generic["tools"].([]any); ok {
		for _, raw := range tools {
			t, _ := raw.(map[string]any)
			fn, _ := t["function"].(map[string]any)
			if s, ok := fn["arguments"].(string); ok {
				var params any
				if json.Unmarshal([]byte(s), &params) == nil {
					fn["parameters"] = params
				}
				delete(fn, "arguments")
			}
		}
	}
	body, _ = json.Marshal(generic)

	idleCtx, idleCancel := context.WithCancel(ctx)
	defer idleCancel()
	idleTimer := time.AfterFunc(o.idleTimeout, idleCancel)
	defer idleTimer.Stop()
	firstTokenTimer := time.AfterFunc(o.firstTokenTimeout, idleCancel)
	defer firstTokenTimer.Stop()
	gotData := false
	httpReq, err := http.NewRequestWithContext(idleCtx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	for k, v := range o.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := o.http.Do(httpReq)
	if err != nil {
		if idleCtx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("%w: no response from model (first token %s / idle %s)", errProviderTimeout, o.firstTokenTimeout, o.idleTimeout)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		body := strings.TrimSpace(string(b))
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return fmt.Errorf("provider authentication failed (HTTP %d): %s — check the API key and that it is exported before starting conclave", resp.StatusCode, body)
		}
		return httpError{Status: resp.StatusCode, Body: body}
	}

	type callState struct {
		ID, Name, Args string
	}
	calls := map[int]*callState{}
	var order []int
	var usage domain.Usage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return fmt.Errorf("provider: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) > 0 || chunk.Usage != nil {
			gotData = true
			firstTokenTimer.Stop()
			idleTimer.Reset(o.idleTimeout)
		}
		if chunk.Usage != nil {
			usage.PromptTokens = chunk.Usage.PromptTokens
			usage.CompletionTokens = chunk.Usage.CompletionTokens
			usage.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			usage.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
		}
		for _, choice := range chunk.Choices {
			d := choice.Delta
			if d.Content != "" {
				if err := sink(Event{Type: EventText, Text: d.Content}); err != nil {
					return err
				}
			}
			if r := d.ReasoningContent; r != "" {
				if err := sink(Event{Type: EventReason, Text: r}); err != nil {
					return err
				}
			} else if r := d.Reasoning; r != "" {
				if err := sink(Event{Type: EventReason, Text: r}); err != nil {
					return err
				}
			}
			for _, tc := range d.ToolCalls {
				idx := tc.Index
				cs := calls[idx]
				if cs == nil {
					cs = &callState{}
					calls[idx] = cs
					order = append(order, idx)
				}
				if tc.ID != "" {
					cs.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cs.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					cs.Args += tc.Function.Arguments
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if idleCtx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("%w: no model response (first token %s / idle %s)", errProviderTimeout, o.firstTokenTimeout, o.idleTimeout)
		}
		return err
	}
	if idleCtx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("%w: no model response (first token %s / idle %s)", errProviderTimeout, o.firstTokenTimeout, o.idleTimeout)
	}
	if !gotData {
		return fmt.Errorf("provider returned an empty response stream")
	}
	sort.Ints(order)
	for _, idx := range order {
		cs := calls[idx]
		if cs.Name == "" {
			continue
		}
		if cs.ID == "" {
			cs.ID = fmt.Sprintf("call_%d", idx)
		}
		if cs.Args == "" {
			cs.Args = "{}"
		}
		if err := sink(Event{Type: EventTool, ToolCall: domain.ToolCall{ID: cs.ID, Name: cs.Name, Arguments: cs.Args}}); err != nil {
			return err
		}
	}
	usage.Requests = 1
	if err := sink(Event{Type: EventUsage, Usage: usage}); err != nil {
		return err
	}
	return sink(Event{Type: EventDone})
}
