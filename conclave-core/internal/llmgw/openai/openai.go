// Package openai implements llmgw.Client for OpenAI-compatible endpoints.
package openai

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

	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/obs"
)

// Client talks to an OpenAI-compatible /chat/completions API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a client for a base URL like https://api.openai.com/v1.
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
	}
}

// Complete implements llmgw.Client.
func (c *Client) Complete(ctx context.Context, req llmgw.Request) (llmgw.Response, error) {
	timer := obs.Default().StartTimer("conclave_llm_request_seconds", "LLM request duration", obs.Labels{"model": req.Model})
	defer timer.Stop()
	status := "error"
	defer func() {
		obs.Default().Counter("conclave_llm_requests_total", "LLM requests by status", obs.Labels{
			"model": req.Model, "status": status,
		}).Inc()
	}()
	body := chatRequest{
		Model:     req.Model,
		Messages:  buildMessages(req),
		Tools:     buildTools(req.Tools),
		MaxTokens: req.MaxTokens,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return llmgw.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return llmgw.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return llmgw.Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return llmgw.Response{}, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return llmgw.Response{}, fmt.Errorf("openai: decode: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return llmgw.Response{}, fmt.Errorf("openai: empty response")
	}
	msg := parsed.Choices[0].Message
	out := llmgw.Message{Role: "assistant", Content: msg.Content}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, llmgw.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	status = "ok"
	obs.Default().Counter("conclave_llm_tokens_total", "LLM tokens", obs.Labels{"model": req.Model, "kind": "prompt"}).Add(float64(parsed.Usage.PromptTokens))
	obs.Default().Counter("conclave_llm_tokens_total", "LLM tokens", obs.Labels{"model": req.Model, "kind": "completion"}).Add(float64(parsed.Usage.CompletionTokens))
	return llmgw.Response{
		Message: out,
		Usage: llmgw.Usage{
			PromptTokens:     parsed.Usage.PromptTokens,
			CompletionTokens: parsed.Usage.CompletionTokens,
		},
	}, nil
}

// Stream performs a streaming completion over SSE, invoking onDelta per chunk.
func (c *Client) Stream(ctx context.Context, req llmgw.Request, onDelta func(llmgw.Delta)) (llmgw.Response, error) {
	timer := obs.Default().StartTimer("conclave_llm_request_seconds", "LLM request duration", obs.Labels{"model": req.Model, "stream": "true"})
	defer timer.Stop()
	status := "error"
	defer func() {
		obs.Default().Counter("conclave_llm_requests_total", "LLM requests by status", obs.Labels{
			"model": req.Model, "status": status,
		}).Inc()
	}()

	body := chatRequest{
		Model:     req.Model,
		Messages:  buildMessages(req),
		Tools:     buildTools(req.Tools),
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return llmgw.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return llmgw.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return llmgw.Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return llmgw.Response{}, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	out := llmgw.Response{Message: llmgw.Message{Role: "assistant"}}
	var content strings.Builder
	toolIndex := map[int]int{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
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
			out.Usage = llmgw.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			content.WriteString(d.Content)
			if onDelta != nil {
				onDelta(llmgw.Delta{Content: d.Content})
			}
		}
		if d.ReasoningContent != "" && onDelta != nil {
			onDelta(llmgw.Delta{Reasoning: d.ReasoningContent})
		}
		for _, tc := range d.ToolCalls {
			pos, ok := toolIndex[tc.Index]
			if !ok {
				pos = len(out.Message.ToolCalls)
				toolIndex[tc.Index] = pos
				out.Message.ToolCalls = append(out.Message.ToolCalls, llmgw.ToolCall{})
			}
			if tc.ID != "" {
				out.Message.ToolCalls[pos].ID = tc.ID
			}
			if tc.Function.Name != "" {
				out.Message.ToolCalls[pos].Name = tc.Function.Name
			}
			out.Message.ToolCalls[pos].Arguments += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return llmgw.Response{}, fmt.Errorf("openai: reading stream: %w", err)
	}
	out.Message.Content = content.String()
	status = "ok"
	obs.Default().Counter("conclave_llm_tokens_total", "LLM tokens", obs.Labels{"model": req.Model, "kind": "prompt"}).Add(float64(out.Usage.PromptTokens))
	obs.Default().Counter("conclave_llm_tokens_total", "LLM tokens", obs.Labels{"model": req.Model, "kind": "completion"}).Add(float64(out.Usage.CompletionTokens))
	return out, nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func buildMessages(req llmgw.Request) []chatMessage {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		cm := chatMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, toolCall{
				ID: tc.ID, Type: "function",
				Function: functionCall{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		msgs = append(msgs, cm)
	}
	return msgs
}

func buildTools(defs []llmgw.ToolDef) []toolDef {
	if len(defs) == 0 {
		return nil
	}
	out := make([]toolDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, toolDef{
			Type: "function",
			Function: functionDef{
				Name: d.Name, Description: d.Description, Parameters: d.Parameters,
			},
		})
	}
	return out
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Tools     []toolDef     `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
