package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Interviewer turns a user's idea into a Spec by asking clarifying questions.
// It is resumable: when it asks a question the interview is persisted with the
// pending question id, and Resume continues once the user answers.
type Interviewer struct {
	Store        store.Store
	Log          *events.Log
	Client       llmgw.Client
	Model        string
	MaxQuestions int
	// Models resolves the interviewer model from live config.
	Models llmgw.ModelResolver
}

func (iv *Interviewer) model() string {
	if iv.Models != nil {
		if m := iv.Models.BrainModel(domain.BrainInterviewer); m != "" {
			return m
		}
	}
	return iv.Model
}

// NewInterviewer creates an interviewer.
func NewInterviewer(st store.Store, log *events.Log, client llmgw.Client, model string) *Interviewer {
	return &Interviewer{Store: st, Log: log, Client: client, Model: model, MaxQuestions: 5}
}

// Start begins an interview for a session.
func (iv *Interviewer) Start(ctx context.Context, sessionID, idea string) (*domain.Interview, *domain.Question, error) {
	if strings.TrimSpace(idea) == "" {
		return nil, nil, fmt.Errorf("interviewer: idea is required")
	}
	in := domain.Interview{
		ID: uuid.NewString(), SessionID: sessionID, Idea: idea,
		State:      domain.InterviewRunning,
		Transcript: []domain.InterviewMessage{{Role: "user", Content: idea}},
		CreatedAt:  time.Now().UTC(),
	}
	if err := iv.Store.CreateInterview(ctx, in); err != nil {
		return nil, nil, err
	}
	if _, err := iv.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventInterviewStarted,
		Payload: map[string]any{"interview_id": in.ID},
	}); err != nil {
		return nil, nil, err
	}
	return iv.step(ctx, in)
}

// Resume continues an interview with the user's answer to the pending question.
func (iv *Interviewer) Resume(ctx context.Context, interviewID string, selected []string, custom string) (*domain.Interview, *domain.Question, error) {
	in, err := iv.Store.GetInterview(ctx, interviewID)
	if err != nil {
		return nil, nil, err
	}
	answer := strings.TrimSpace(strings.Join(selected, ", "))
	if custom != "" {
		if answer != "" {
			answer += " — "
		}
		answer += custom
	}
	if answer == "" {
		answer = "(no answer)"
	}
	in.Transcript = append(in.Transcript, domain.InterviewMessage{Role: "user", Content: answer})
	in.State = domain.InterviewRunning
	in.PendingQuestionID = ""
	return iv.step(ctx, *in)
}

func (iv *Interviewer) step(ctx context.Context, in domain.Interview) (*domain.Interview, *domain.Question, error) {
	if iv.Client == nil {
		return nil, nil, fmt.Errorf("interviewer: no LLM client configured")
	}
	ctx = WithSessionID(ctx, in.SessionID)
	resp, err := iv.Client.Complete(ctx, llmgw.Request{
		Model:    iv.model(),
		System:   interviewerSystemPrompt(iv.MaxQuestions),
		Messages: interviewMessages(in.Transcript),
		Tools:    interviewerTools(),
	})
	if err != nil {
		in.State = domain.InterviewFailed
		_ = iv.Store.UpdateInterview(ctx, in)
		return nil, nil, err
	}

	for _, tc := range resp.Message.ToolCalls {
		switch tc.Name {
		case "ask":
			return iv.ask(ctx, in, json.RawMessage(tc.Arguments))
		case "write_spec":
			return iv.writeSpec(ctx, in, json.RawMessage(tc.Arguments))
		}
	}
	if strings.TrimSpace(resp.Message.Content) != "" {
		return iv.writeSpec(ctx, in, mustJSON(map[string]string{"content": resp.Message.Content}))
	}
	return nil, nil, fmt.Errorf("interviewer: model returned no action")
}

func (iv *Interviewer) ask(ctx context.Context, in domain.Interview, args json.RawMessage) (*domain.Interview, *domain.Question, error) {
	var parsed struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return nil, nil, fmt.Errorf("interviewer: ask arguments: %w", err)
	}
	if strings.TrimSpace(parsed.Question) == "" {
		return nil, nil, fmt.Errorf("interviewer: ask requires a question")
	}
	options := make([]domain.Option, 0, len(parsed.Options))
	for i, o := range parsed.Options {
		options = append(options, domain.Option{Label: o, Recommended: i == 0})
	}
	q := domain.Question{
		ID: uuid.NewString(), SessionID: in.SessionID, Kind: domain.QuestionKindInterview,
		Ref: in.ID, Text: parsed.Question, Options: options,
		AutoPolicy: domain.AutoInherit, State: domain.QuestionOpen, CreatedAt: time.Now().UTC(),
	}
	if err := iv.Store.CreateQuestion(ctx, q); err != nil {
		return nil, nil, err
	}
	in.Transcript = append(in.Transcript, domain.InterviewMessage{Role: "question", Content: parsed.Question})
	in.State = domain.InterviewWaiting
	in.PendingQuestionID = q.ID
	if err := iv.Store.UpdateInterview(ctx, in); err != nil {
		return nil, nil, err
	}
	if _, err := iv.Log.Append(ctx, domain.Event{
		SessionID: in.SessionID, Type: domain.EventQuestionAsked,
		Payload: map[string]any{"question_id": q.ID, "kind": domain.QuestionKindInterview, "text": parsed.Question},
	}); err != nil {
		return nil, nil, err
	}
	return &in, &q, nil
}

func (iv *Interviewer) writeSpec(ctx context.Context, in domain.Interview, args json.RawMessage) (*domain.Interview, *domain.Question, error) {
	var parsed struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return nil, nil, fmt.Errorf("interviewer: write_spec arguments: %w", err)
	}
	content := strings.TrimSpace(parsed.Content)
	if content == "" {
		return nil, nil, fmt.Errorf("interviewer: spec content is empty")
	}
	version := 1
	if prev, err := iv.Store.LatestSpec(ctx, in.SessionID); err == nil {
		version = prev.Version + 1
	}
	spec := domain.Spec{
		ID: uuid.NewString(), SessionID: in.SessionID, Version: version,
		Content: content, CreatedAt: time.Now().UTC(),
	}
	if err := iv.Store.CreateSpec(ctx, spec); err != nil {
		return nil, nil, err
	}
	in.Transcript = append(in.Transcript, domain.InterviewMessage{Role: "assistant", Content: content})
	in.State = domain.InterviewDone
	in.PendingQuestionID = ""
	in.SpecID = spec.ID
	if err := iv.Store.UpdateInterview(ctx, in); err != nil {
		return nil, nil, err
	}
	if _, err := iv.Log.Append(ctx, domain.Event{
		SessionID: in.SessionID, Type: domain.EventSpecCreated,
		Payload: map[string]any{"version": version, "interview_id": in.ID},
	}); err != nil {
		return nil, nil, err
	}
	return &in, nil, nil
}

func interviewerSystemPrompt(maxQuestions int) string {
	return fmt.Sprintf(`You are the requirements interviewer for a software project.

Your job is to turn the user's idea into a clear, actionable spec. Ask focused
clarifying questions one at a time, and only when the answer materially changes
the plan (stack, scope, existing assets, constraints, acceptance criteria). Ask
at most %d questions total. Offer concrete options and put the recommended one
first. When you have enough information, call write_spec with the spec in
markdown (goals, scope, constraints, acceptance criteria).`, maxQuestions)
}

func interviewMessages(transcript []domain.InterviewMessage) []llmgw.Message {
	out := make([]llmgw.Message, 0, len(transcript))
	for _, m := range transcript {
		role := m.Role
		switch role {
		case "question", "assistant":
			role = "assistant"
		default:
			role = "user"
		}
		out = append(out, llmgw.Message{Role: role, Content: m.Content})
	}
	return out
}

func interviewerTools() []llmgw.ToolDef {
	ask := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"question"},
	}
	spec := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{"type": "string", "description": "The spec in markdown."},
		},
		"required": []string{"content"},
	}
	return []llmgw.ToolDef{
		{Name: "ask", Description: "Ask the user a clarifying question.", Parameters: mustJSON(ask)},
		{Name: "write_spec", Description: "Finish the interview with the spec.", Parameters: mustJSON(spec)},
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
