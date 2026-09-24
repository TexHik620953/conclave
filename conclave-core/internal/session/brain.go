package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

var (
	reParenNum = regexp.MustCompile(`\s*\((\d{1,2})\)\s*`)
	reNumParen = regexp.MustCompile(`\s+(\d{1,2})\)\s+`)
)

// formatQuestionText adds line breaks before inline enumerations like "(1)"
// or "2)" so a question the model sent as one long line renders readably.
func formatQuestionText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "\n") {
		return s
	}
	s = reParenNum.ReplaceAllString(s, "\n($1) ")
	s = reNumParen.ReplaceAllString(s, "\n$1) ")
	return strings.TrimSpace(s)
}

// Controller is the single top-level "brain" of a session. It receives user
// messages and decides, in one loop, whether to ask a clarifying question,
// write a spec, build a plan, run it, revise it or finish. It re-enters after
// every user answer and after each plan run.
type Controller struct {
	Store    store.Store
	Service  *Service
	Client   llmgw.Client
	Model    string
	MaxSteps int
	// Models resolves the controller model from the live configuration; when nil
	// or empty, Model is used.
	Models llmgw.ModelResolver
}

// NewController creates the session controller.
func NewController(svc *Service, client llmgw.Client, model string) *Controller {
	return &Controller{Store: svc.Store, Service: svc, Client: client, Model: model, MaxSteps: 4}
}

// model returns the model reference to use for a decision.
func (c *Controller) model() string {
	if c.Models != nil {
		if m := c.Models.BrainModel(domain.BrainController); m != "" {
			return m
		}
	}
	return c.Model
}

// Step runs one controller decision cycle for a session. It consumes any
// pending user messages, then either asks the user (and returns), records a
// spec/plan and enqueues execution, or finishes the session.
func (c *Controller) Step(ctx context.Context, sessionID string) error {
	if c.Client == nil {
		return fmt.Errorf("controller: no LLM client configured")
	}
	ctx = WithSessionID(ctx, sessionID)
	sess, err := c.Store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Status == domain.SessionDone || sess.Status == domain.SessionCancelled {
		return nil
	}

	// Name the session from the first user message (once).
	if strings.TrimSpace(sess.Title) == "" {
		if title, terr := c.summarizeTitle(ctx, sessionID); terr == nil && title != "" {
			if err := c.Store.UpdateSessionTitle(ctx, sessionID, title); err == nil {
				sess.Title = title
				_, _ = c.Service.Log.Append(ctx, domain.Event{
					SessionID: sessionID, Type: domain.EventSessionRenamed,
					Payload: map[string]any{"title": title},
				})
			}
		}
	}

	questions, err := c.Store.ListQuestions(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if q.State == domain.QuestionOpen {
			return nil // wait for the user's answer
		}
	}

	pending, err := c.Store.PendingMessages(ctx, sessionID)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		ids := make([]string, 0, len(pending))
		for _, m := range pending {
			ids = append(ids, m.ID)
		}
		if err := c.Store.MarkMessagesInjected(ctx, ids); err != nil {
			return err
		}
	}

	maxSteps := c.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 4
	}
	for step := 0; step < maxSteps; step++ {
		spec, _ := c.Store.LatestSpec(ctx, sessionID)
		plan, _ := c.Store.ActivePlan(ctx, sessionID)
		var nodes []domain.PlanNode
		var edges []domain.PlanEdge
		if plan != nil {
			nodes, _ = c.Store.ListPlanNodes(ctx, plan.ID)
			edges, _ = c.Store.ListPlanEdges(ctx, plan.ID)
		}

		resp, err := c.Client.Complete(ctx, llmgw.Request{
			Model:    c.model(),
			System:   c.systemPrompt(),
			Messages: []llmgw.Message{{Role: "user", Content: c.renderState(ctx, sess, spec, plan, nodes, edges, questions, pending)}},
			Tools:    controllerTools(),
		})
		if err != nil {
			return err
		}
		dec := parseControllerDecision(resp.Message)
		// Discovery guard: never build or run a plan before a spec exists.
		if spec == nil && (dec.Kind == "set_plan" || dec.Kind == "run_plan" || dec.Kind == "replan") {
			return c.ask(ctx, sessionID, []controllerQuestion{{
				Text: "Before I build a plan: what is the goal, who are the users, and what are the key constraints and definition of done?",
			}})
		}
		switch dec.Kind {
		case "ask":
			return c.ask(ctx, sessionID, dec.Questions)
		case "write_spec":
			if _, err := c.Service.AddSpec(ctx, sessionID, dec.Spec); err != nil {
				return err
			}
			continue
		case "set_plan":
			in := dec.Plan
			in.SessionID = sessionID
			var p *domain.Plan
			if plan != nil {
				p, err = c.Service.RevisePlan(ctx, sessionID, plan.ID, in)
			} else {
				p, err = c.Service.CreatePlan(ctx, in)
			}
			if err != nil {
				return err
			}
			_, err = c.Service.EnqueuePlan(ctx, sessionID, p.ID)
			return err
		case "run_plan":
			if plan != nil && plan.Status == domain.PlanActive {
				_, err := c.Service.EnqueuePlan(ctx, sessionID, plan.ID)
				return err
			}
			continue // no runnable plan; let the model choose another action
		case "replan":
			if plan == nil {
				continue
			}
			revised, err := c.Service.Replan(ctx, sessionID, plan.ID, dec.Reason)
			if err != nil {
				return err
			}
			_, err = c.Service.EnqueuePlan(ctx, sessionID, revised.ID)
			return err
		case "finish":
			// Never finish before any work exists: treat it as a first
			// clarification instead (models sometimes try to finish early).
			if spec == nil && plan == nil {
				text := strings.TrimSpace(dec.Summary)
				if text == "" {
					text = "Before I can finish, what is the goal, who are the users, and what are the key constraints and definition of done?"
				}
				return c.ask(ctx, sessionID, []controllerQuestion{{Text: text}})
			}
			return c.finish(ctx, sessionID, dec.Summary)
		default:
			content := strings.TrimSpace(resp.Message.Content)
			if content == "" {
				return nil
			}
			// A plain-text reply is not a completion; surface it as a question.
			if plan != nil && plan.Status == domain.PlanDone {
				return c.finish(ctx, sessionID, content)
			}
			return c.ask(ctx, sessionID, []controllerQuestion{{Text: content}})
		}
	}
	return nil
}

// ask creates one question per entry (a short questionnaire is allowed).
func (c *Controller) ask(ctx context.Context, sessionID string, questions []controllerQuestion) error {
	created := 0
	for _, item := range questions {
		text := formatQuestionText(item.Text)
		if text == "" {
			continue
		}
		opts := make([]domain.Option, 0, len(item.Options))
		for i, o := range item.Options {
			if strings.TrimSpace(o) == "" {
				continue
			}
			opts = append(opts, domain.Option{Label: o, Recommended: i == 0})
		}
		q := domain.Question{
			ID: uuid.NewString(), SessionID: sessionID, Kind: domain.QuestionKindController,
			Text: text, Options: opts, AutoPolicy: domain.AutoInherit,
			State: domain.QuestionOpen, CreatedAt: time.Now().UTC(),
		}
		if err := c.Store.CreateQuestion(ctx, q); err != nil {
			return err
		}
		if _, err := c.Service.Log.Append(ctx, domain.Event{
			SessionID: sessionID, Type: domain.EventQuestionAsked,
			Payload: map[string]any{"question_id": q.ID, "kind": domain.QuestionKindController, "text": text},
		}); err != nil {
			return err
		}
		created++
	}
	if created == 0 {
		return fmt.Errorf("controller: ask requires a question")
	}
	return nil
}

// summarizeTitle asks the LLM for a short title based on the first user message.
func (c *Controller) summarizeTitle(ctx context.Context, sessionID string) (string, error) {
	msgs, err := c.Store.ListMessages(ctx, sessionID, 50)
	if err != nil {
		return "", err
	}
	first := ""
	for _, m := range msgs {
		if m.Kind == domain.MessageKindUser && strings.TrimSpace(m.Content) != "" {
			first = m.Content
			break
		}
	}
	if first == "" {
		return "", nil
	}
	resp, err := c.Client.Complete(ctx, llmgw.Request{
		Model:  c.model(),
		System: "Summarize the user's task into a short title of at most 6 words. Reply with the title only, no quotes or punctuation at the ends.",
		Messages: []llmgw.Message{
			{Role: "user", Content: first},
		},
	})
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(resp.Message.Content)
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(strings.Trim(title, "\"'`"))
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	return title, nil
}

func (c *Controller) finish(ctx context.Context, sessionID, summary string) error {
	if err := c.Store.UpdateSessionStatus(ctx, sessionID, domain.SessionDone); err != nil {
		return err
	}
	_, err := c.Service.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventSessionDone,
		Payload: map[string]any{"summary": summary},
	})
	return err
}

type controllerQuestion struct {
	Text    string
	Options []string
}

type controllerDecision struct {
	Kind      string
	Questions []controllerQuestion
	Spec      string
	Plan      CreatePlanInput
	Reason    string
	Summary   string
}

func parseControllerDecision(msg llmgw.Message) controllerDecision {
	for _, tc := range msg.ToolCalls {
		switch tc.Name {
		case "ask":
			var parsed struct {
				Question  string   `json:"question"`
				Options   []string `json:"options"`
				Questions []struct {
					Question string   `json:"question"`
					Options  []string `json:"options"`
				} `json:"questions"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				var qs []controllerQuestion
				if strings.TrimSpace(parsed.Question) != "" {
					qs = append(qs, controllerQuestion{Text: parsed.Question, Options: parsed.Options})
				}
				for _, q := range parsed.Questions {
					if strings.TrimSpace(q.Question) != "" {
						qs = append(qs, controllerQuestion{Text: q.Question, Options: q.Options})
					}
				}
				return controllerDecision{Kind: "ask", Questions: qs}
			}
		case "write_spec":
			var parsed struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				return controllerDecision{Kind: "write_spec", Spec: parsed.Content}
			}
		case "set_plan":
			in, err := parsePlan(msg)
			if err == nil {
				return controllerDecision{Kind: "set_plan", Plan: in}
			}
		case "run_plan":
			return controllerDecision{Kind: "run_plan"}
		case "replan":
			var parsed struct {
				Reason string `json:"reason"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				return controllerDecision{Kind: "replan", Reason: parsed.Reason}
			}
		case "finish":
			var parsed struct {
				Summary string `json:"summary"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				return controllerDecision{Kind: "finish", Summary: parsed.Summary}
			}
		}
	}
	return controllerDecision{}
}

func (c *Controller) renderState(ctx context.Context, sess *domain.Session, spec *domain.Spec, plan *domain.Plan, nodes []domain.PlanNode, edges []domain.PlanEdge, questions []domain.Question, pending []domain.Message) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Session\nTitle: %s\nStatus: %s\n\n", sess.Title, sess.Status)

	sb.WriteString("## Spec\n")
	if spec != nil && strings.TrimSpace(spec.Content) != "" {
		sb.WriteString(spec.Content)
	} else {
		sb.WriteString("(no spec yet)")
	}
	sb.WriteString("\n\n")

	sb.WriteString("## Plan\n")
	if plan == nil {
		sb.WriteString("(no plan yet)")
	} else {
		fmt.Fprintf(&sb, "version=%d status=%s\n", plan.Version, plan.Status)
		for _, n := range nodes {
			fmt.Fprintf(&sb, "- %s [%s] %s state=%s", n.Key, n.Kind, n.Title, n.State)
			if n.Output != "" {
				out := n.Output
				if len(out) > 300 {
					out = out[:300]
				}
				fmt.Fprintf(&sb, " output=%q", out)
			}
			sb.WriteString("\n")
		}
		for _, e := range edges {
			fmt.Fprintf(&sb, "- edge %s -> %s (%s)\n", e.From, e.To, e.Kind)
		}
	}
	sb.WriteString("\n")

	if len(questions) > 0 {
		sb.WriteString("## Dialogue\n")
		start := 0
		if len(questions) > 10 {
			start = len(questions) - 10
		}
		for _, q := range questions[start:] {
			fmt.Fprintf(&sb, "- Q(%s,%s): %s", q.ID, q.State, q.Text)
			if q.State == domain.QuestionAnswered {
				if a, err := c.Store.GetAnswer(ctx, q.ID); err == nil {
					fmt.Fprintf(&sb, " => %s %s", strings.Join(a.Selected, ", "), a.Custom)
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(pending) > 0 {
		sb.WriteString("## New user messages\n")
		for _, m := range pending {
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// orderedPlaybooks lists roles in software-lifecycle order so the model plans
// discovery (BA/requirements) before architecture and implementation.
func (c *Controller) orderedPlaybooks() []playbook.Playbook {
	pbs := c.Service.Playbooks.List()
	order := map[string]int{
		"ba": 0, "architecture": 1, "design": 2, "dba": 3,
		"backend": 4, "frontend": 5, "security_review": 6, "general": 7,
	}
	sort.SliceStable(pbs, func(i, j int) bool {
		oi, iok := order[pbs[i].ID]
		oj, jok := order[pbs[j].ID]
		if iok && jok {
			return oi < oj
		}
		if iok != jok {
			return iok
		}
		return pbs[i].ID < pbs[j].ID
	})
	return pbs
}

func (c *Controller) systemPrompt() string {
	var sb strings.Builder
	sb.WriteString(`You are the session controller for a software project. You drive a session from the user's goal to completion with a single loop.

Decide the single best next action and call exactly one tool:
- ask: ask the user one focused clarifying question when the answer materially changes the work.
- write_spec: record the requirements spec (markdown) when you have enough context.
- set_plan: build the execution plan graph from the spec.
- run_plan: run the current plan.
- replan: revise the current plan when results or new user messages require it.
- finish: end the session when the goal is met or no further work is possible.

Guidelines:
- Do NOT finish on the first turn: a session with no spec must first ask clarifying questions or write a spec. Never call finish before the goal is actually achieved.
- Discovery first: never build or run a plan before a spec exists.
- For a new, broad or ambiguous request (e.g. a product idea), ask focused clarifying questions FIRST (goal, users, scope, constraints, monetization, definition of done) before writing the spec. Do not jump to architecture or implementation on the first message.
- Ask ONE question at a time; if you need several answers, send a short questionnaire via the "questions" array (each item is a separate card). Never cram many numbered sub-questions into a single "question".
- Whenever a question has a small set of plausible answers, provide 2-5 concrete "options" (put the recommended one first) so the user can pick instead of typing.
- Write the spec once the request is clear enough, then build the plan.
- When building a plan, order the work by the software lifecycle (unless the task is trivial or purely technical):
  1) requirements / business analysis (ba),
  2) architecture and UX design (architecture, design),
  3) data and implementation (dba, backend, frontend),
  4) review and security (security_review), then finish.
- Assign each node a role and a grade (playbook_id is the role key; use the grade names listed below). Give each node a short human-readable id like "ba", "arch", "api" — never a UUID.
- After a plan finishes, review the node states/outputs, then run_plan again, replan, or finish.
- Consider the new user messages injected below before deciding.

Available roles and grades (use playbook_id=<role key>, grade=<grade>):
`)
	for _, pb := range c.orderedPlaybooks() {
		fmt.Fprintf(&sb, "- %s — %s", pb.ID, pb.Title)
		if len(pb.Inputs) > 0 {
			fmt.Fprintf(&sb, " (inputs: %s)", strings.Join(pb.Inputs, ", "))
		}
		if len(pb.Outputs) > 0 {
			fmt.Fprintf(&sb, " (outputs: %s)", strings.Join(pb.Outputs, ", "))
		}
		sb.WriteString("\n")
		for _, g := range pb.Roles {
			fmt.Fprintf(&sb, "    grade %s (model %s)\n", g.Tier, g.Model)
		}
	}
	return sb.String()
}

func controllerTools() []llmgw.ToolDef {
	question := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string", "description": "The question text."},
			"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "2-5 concrete answer options; recommended first."},
		},
		"required": []string{"question"},
	}
	ask := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question":  map[string]any{"type": "string", "description": "A single question (shorthand for one item in `questions`)."},
			"options":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"questions": map[string]any{"type": "array", "items": question, "description": "A short questionnaire; each item becomes its own card."},
		},
	}
	spec := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{"type": "string", "description": "The spec in markdown."},
		},
		"required": []string{"content"},
	}
	replan := map[string]any{
		"type":       "object",
		"properties": map[string]any{"reason": map[string]any{"type": "string"}},
		"required":   []string{"reason"},
	}
	finish := map[string]any{
		"type":       "object",
		"properties": map[string]any{"summary": map[string]any{"type": "string"}},
	}
	tools := []llmgw.ToolDef{
		{Name: "ask", Description: "Ask the user a clarifying question.", Parameters: mustJSON(ask)},
		{Name: "write_spec", Description: "Record the requirements spec.", Parameters: mustJSON(spec)},
	}
	tools = append(tools, plannerTools()...)
	tools = append(tools,
		llmgw.ToolDef{Name: "run_plan", Description: "Run the current plan.", Parameters: mustJSON(map[string]any{"type": "object"})},
		llmgw.ToolDef{Name: "replan", Description: "Revise the current plan.", Parameters: mustJSON(replan)},
		llmgw.ToolDef{Name: "finish", Description: "Finish the session.", Parameters: mustJSON(finish)},
	)
	return tools
}
