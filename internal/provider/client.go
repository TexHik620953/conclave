package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to an OpenAI-compatible HTTP API.
type Client struct {
	name    string
	baseURL string
	apiKey  string
	headers map[string]string
	http    *http.Client
}

// Option customises a Client.
type Option func(*Client)

// WithHeaders sets extra request headers.
func WithHeaders(h map[string]string) Option { return func(c *Client) { c.headers = h } }

// WithHTTPClient overrides the underlying HTTP client.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// New creates a client for the given base URL (e.g. http://localhost:11434/v1).
func New(name, baseURL, apiKey string, timeout time.Duration, opts ...Option) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	c := &Client{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		headers: map[string]string{},
		http:    &http.Client{Timeout: timeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name returns the provider name.
func (c *Client) Name() string { return c.name }

// Chat performs a non-streaming completion.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.apiError(resp)
	}
	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decoding response: %w", c.name, err)
	}
	return &out, nil
}

// ChatStream performs a streaming completion, invoking onDelta for each chunk.
// It returns the fully aggregated response.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onDelta func(Delta)) (*ChatResponse, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.apiError(resp)
	}

	out := &ChatResponse{Model: req.Model}
	out.Choices = []Choice{{Index: 0}}
	agg := &out.Choices[0].Message
	agg.Role = "assistant"
	toolIndex := map[int]int{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			out.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			out.Choices[0].FinishReason = ch.FinishReason
		}
		d := Delta{Content: ch.Delta.Content, Reasoning: ch.Delta.ReasoningContent}
		if d.Content != "" {
			agg.Content += d.Content
		}
		for _, tc := range ch.Delta.ToolCalls {
			idx := tc.Index
			pos, ok := toolIndex[idx]
			if !ok {
				pos = len(agg.ToolCalls)
				toolIndex[idx] = pos
				agg.ToolCalls = append(agg.ToolCalls, ToolCall{Type: "function"})
			}
			if tc.ID != "" {
				agg.ToolCalls[pos].ID = tc.ID
			}
			if tc.Type != "" {
				agg.ToolCalls[pos].Type = tc.Type
			}
			if tc.Function.Name != "" {
				agg.ToolCalls[pos].Function.Name = tc.Function.Name
			}
			agg.ToolCalls[pos].Function.Arguments += tc.Function.Arguments
			d.ToolCalls = append(d.ToolCalls, ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		if onDelta != nil && (d.Content != "" || len(d.ToolCalls) > 0 || d.Reasoning != "") {
			onDelta(d)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: reading stream: %w", c.name, err)
	}
	for i := range agg.ToolCalls {
		if agg.ToolCalls[i].ID == "" {
			agg.ToolCalls[i].ID = fmt.Sprintf("call_%d", i)
		}
		if agg.ToolCalls[i].Type == "" {
			agg.ToolCalls[i].Type = "function"
		}
	}
	return out, nil
}

// ListModels returns the model identifiers advertised by the provider.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.apiError(resp)
	}
	var out ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}

func (c *Client) apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Error.Message != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: parsed.Error.Message, RetryAfter: retryAfter}
	}
	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		msg = resp.Status
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg, RetryAfter: retryAfter}
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}
