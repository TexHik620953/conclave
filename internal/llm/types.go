package llm

import (
	"context"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	Messages    []domain.Message
	Tools       []Tool
	Effort      string
	Temperature float64
	MaxTokens   int
}

type EventType string

const (
	EventText   EventType = "text"
	EventReason EventType = "reasoning"
	EventUsage  EventType = "usage"
	EventTool   EventType = "tool"
	EventDone   EventType = "done"
)

type Event struct {
	Type     EventType
	Text     string
	ToolCall domain.ToolCall
	Usage    domain.Usage
	Err      error
}

type Model struct {
	Ref          string
	Provider     string
	ID           string
	Context      int
	MaxOutput    int
	Capabilities config.Capabilities
	EffortMap    map[string]string
}

func ModelFromRef(ref string, m config.ModelRef) Model {
	return Model{
		Ref: ref, Provider: m.Provider, ID: m.ID,
		Context: m.Config.Context, MaxOutput: m.Config.MaxOutput,
		Capabilities: m.Config.Capabilities,
		EffortMap:    m.Config.EffortMap,
	}
}

// Client is the provider boundary. Implementations must be safe for concurrent use.
type Client interface {
	Stream(ctx context.Context, model Model, req Request, sink func(Event) error) error
}

type CollectResult struct {
	Content   string
	Reasoning string
	ToolCalls []domain.ToolCall
	Usage     domain.Usage
}

func Collect(ctx context.Context, c Client, model Model, req Request) (CollectResult, error) {
	var out CollectResult
	err := c.Stream(ctx, model, req, func(e Event) error {
		switch e.Type {
		case EventText:
			out.Content += e.Text
		case EventReason:
			out.Reasoning += e.Text
		case EventTool:
			out.ToolCalls = append(out.ToolCalls, e.ToolCall)
		case EventUsage:
			out.Usage.Add(e.Usage)
		}
		return nil
	})
	return out, err
}
