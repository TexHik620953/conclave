package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNoInteractiveUser is returned by an Asker when no human can answer.
var ErrNoInteractiveUser = errors.New("no interactive user available")

// Option is a single choice presented to the user.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is a request for user input.
type Question struct {
	Header      string   `json:"header,omitempty"`
	Question    string   `json:"question"`
	Options     []Option `json:"options,omitempty"`
	Multiple    bool     `json:"multiple,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
}

// Answer is the user's response to a Question.
type Answer struct {
	Selected []string `json:"selected,omitempty"`
	Custom   string   `json:"custom,omitempty"`
}

// Asker prompts a human for input.
type Asker interface {
	Ask(ctx context.Context, q Question) (Answer, error)
}

type askUser struct{ env *Env }

func (t *askUser) Name() string { return "ask_user" }
func (t *askUser) Description() string {
	return "Ask the user a question. Supports single choice, multiple choice and a custom free-form answer. Use when a decision is genuinely ambiguous."
}
func (t *askUser) Schema() json.RawMessage {
	return schema(map[string]any{
		"header":   map[string]any{"type": "string", "description": "Short label for the question."},
		"question": map[string]any{"type": "string"},
		"options": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":       map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
				},
				"required": []string{"label"},
			},
		},
		"multiple":     map[string]any{"type": "boolean", "description": "Allow selecting several options."},
		"allow_custom": map[string]any{"type": "boolean", "description": "Offer a free-form 'other' answer."},
	}, "question")
}

func (t *askUser) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if t.env.Asker == nil {
		return Result{Content: "no interactive user is available; decide autonomously", IsError: true}, nil
	}
	var q Question
	if err := json.Unmarshal(args, &q); err != nil {
		return Result{}, err
	}
	if q.Question == "" {
		return Result{Content: "question is required", IsError: true}, nil
	}
	ans, err := t.env.Asker.Ask(ctx, q)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	var b strings.Builder
	if len(ans.Selected) > 0 {
		fmt.Fprintf(&b, "selected: %s", strings.Join(ans.Selected, ", "))
	}
	if ans.Custom != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "custom: %s", ans.Custom)
	}
	if b.Len() == 0 {
		return Result{Content: "(no answer)"}, nil
	}
	return Result{Content: b.String()}, nil
}
