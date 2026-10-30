package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
)

type Config struct {
	AgentID     string
	SessionID   string
	RunID       string
	TaskID      string
	Model       llm.Model
	Client      llm.Client
	System      string
	Effort      string
	Temperature float64
	MaxSteps    int
	Cwd         string
	Mode        domain.Mode
	Permissions tools.Env
	Tools       *tools.Registry
	Events      *event.Bus
	OnEvent     func(domain.Event)
	OnMessage   func(domain.Message)
}

type Result struct {
	Messages         []domain.Message
	Usage            domain.Usage
	Records          []domain.UsageRecord
	LastPromptTokens int
}

type Runtime struct {
	cfg Config
}

func New(cfg Config) *Runtime {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 120
	}
	if cfg.AgentID == "" {
		cfg.AgentID = domain.NewID("agent")
	}
	return &Runtime{cfg: cfg}
}

func (r *Runtime) AgentID() string { return r.cfg.AgentID }

func (r *Runtime) Run(ctx context.Context, history []domain.Message) (Result, error) {
	msgs := make([]domain.Message, 0, len(history)+4)
	if r.cfg.System != "" {
		msgs = append(msgs, domain.Message{Role: domain.RoleSystem, Content: r.cfg.System, CreatedAt: time.Now()})
	}
	msgs = append(msgs, history...)
	var total domain.Usage
	var added []domain.Message
	var records []domain.UsageRecord
	lastPrompt := 0

	r.publish("agent.started", "", map[string]any{"agent_id": r.cfg.AgentID, "model": r.cfg.Model.Ref, "task_id": r.cfg.TaskID})
	defer func() {
		r.publish("agent.finished", "", map[string]any{"agent_id": r.cfg.AgentID, "usage": total})
	}()

	reqTools := r.cfg.Tools.Specs()
	for step := 0; step < r.cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return Result{Messages: added, Usage: total, Records: records, LastPromptTokens: lastPrompt}, err
		}
		var content, reasoning strings.Builder
		var calls []domain.ToolCall
		var stepUsage domain.Usage
		r.publish("assistant.started", "", map[string]any{"agent_id": r.cfg.AgentID, "step": step})
		err := r.cfg.Client.Stream(ctx, r.cfg.Model, llm.Request{
			Messages:    msgs,
			Tools:       reqTools,
			Effort:      r.cfg.Effort,
			Temperature: r.cfg.Temperature,
		}, func(e llm.Event) error {
			switch e.Type {
			case llm.EventText:
				content.WriteString(e.Text)
				r.publish("assistant.delta", e.Text, map[string]any{"agent_id": r.cfg.AgentID, "step": step})
			case llm.EventReason:
				reasoning.WriteString(e.Text)
				r.publish("assistant.reasoning", e.Text, map[string]any{"agent_id": r.cfg.AgentID, "step": step})
			case llm.EventTool:
				calls = append(calls, e.ToolCall)
			case llm.EventUsage:
				stepUsage = e.Usage
			}
			return nil
		})
		if err != nil {
			if content.Len() > 0 || reasoning.Len() > 0 || len(calls) > 0 {
				partial := domain.Message{ID: domain.NewID("msg"), Role: domain.RoleAssistant, Content: content.String(), Reasoning: reasoning.String(), ToolCalls: calls, CreatedAt: time.Now()}
				msgs = append(msgs, partial)
				added = append(added, partial)
				if r.cfg.OnMessage != nil {
					r.cfg.OnMessage(partial)
				}
				r.publish("assistant.completed", "", partial)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				r.publish("agent.cancelled", err.Error(), map[string]any{"agent_id": r.cfg.AgentID})
			} else {
				r.publish("agent.error", err.Error(), map[string]any{"agent_id": r.cfg.AgentID})
			}
			return Result{Messages: added, Usage: total, Records: records, LastPromptTokens: lastPrompt}, err
		}
		total.Add(stepUsage)
		if stepUsage.PromptTokens > 0 {
			lastPrompt = stepUsage.PromptTokens
		}
		rec := domain.UsageRecord{
			Provider: r.cfg.Model.Provider, Model: r.cfg.Model.ID,
			AgentID: r.cfg.AgentID, TaskID: r.cfg.TaskID,
			InputTokens: stepUsage.PromptTokens, OutputTokens: stepUsage.CompletionTokens,
			CachedTokens: stepUsage.CachedTokens, ReasoningTokens: stepUsage.ReasoningTokens,
			Requests: stepUsage.Requests, LastContext: stepUsage.PromptTokens,
			ContextWindow: r.cfg.Model.Context,
		}
		records = append(records, rec)
		r.publish("usage.updated", "", rec)
		assistant := domain.Message{
			ID: domain.NewID("msg"), Role: domain.RoleAssistant,
			Content: content.String(), Reasoning: reasoning.String(),
			ToolCalls: calls, CreatedAt: time.Now(),
		}
		msgs = append(msgs, assistant)
		added = append(added, assistant)
		if r.cfg.OnMessage != nil {
			r.cfg.OnMessage(assistant)
		}
		r.publish("assistant.completed", "", assistant)
		if len(calls) == 0 {
			return Result{Messages: added, Usage: total, Records: records, LastPromptTokens: lastPrompt}, nil
		}
		results := r.runToolBatch(ctx, calls)
		for _, res := range results {
			toolMsg := domain.Message{
				ID: domain.NewID("tool"), Role: domain.RoleTool,
				ToolCallID: res.call.ID, Name: res.call.Name, Content: res.text, CreatedAt: time.Now(),
			}
			if res.err != nil {
				toolMsg.Content = "ERROR: " + res.err.Error() + "\n" + res.text
				r.publish("tool.failed", res.err.Error(), map[string]any{"agent_id": r.cfg.AgentID, "name": res.call.Name, "call_id": res.call.ID, "batch_id": res.batchID})
			} else {
				r.publish("tool.completed", res.text, map[string]any{"agent_id": r.cfg.AgentID, "name": res.call.Name, "call_id": res.call.ID, "batch_id": res.batchID})
			}
			msgs = append(msgs, toolMsg)
			added = append(added, toolMsg)
			if r.cfg.OnMessage != nil {
				r.cfg.OnMessage(toolMsg)
			}
		}
	}
	err := fmt.Errorf("agent exceeded maximum steps (%d)", r.cfg.MaxSteps)
	r.publish("agent.error", err.Error(), nil)
	return Result{Messages: added, Usage: total, Records: records, LastPromptTokens: lastPrompt}, err
}

type toolOutcome struct {
	call    domain.ToolCall
	text    string
	err     error
	batchID string
}

func (r *Runtime) runToolBatch(ctx context.Context, calls []domain.ToolCall) []toolOutcome {
	out := make([]toolOutcome, len(calls))
	if len(calls) == 0 {
		return out
	}
	batchID := ""
	if len(calls) > 1 {
		batchID = domain.NewID("batch")
		r.publish("tools.batch.started", "", map[string]any{"agent_id": r.cfg.AgentID, "batch_id": batchID, "calls": calls})
	}
	runOne := func(i int) {
		call := calls[i]
		r.publish("tool.started", "", map[string]any{"agent_id": r.cfg.AgentID, "call": call, "batch_id": batchID})
		text, err := r.runTool(ctx, call)
		out[i] = toolOutcome{call: call, text: text, err: err, batchID: batchID}
	}
	runParallel := func(indexes []int) {
		if len(indexes) == 0 {
			return
		}
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, i := range indexes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				defer func() {
					if rec := recover(); rec != nil {
						out[i] = toolOutcome{call: calls[i], err: fmt.Errorf("tool %s panicked: %v", calls[i].Name, rec), batchID: batchID}
					}
				}()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					out[i] = toolOutcome{call: calls[i], err: ctx.Err(), batchID: batchID}
					return
				}
				runOne(i)
			}(i)
		}
		wg.Wait()
	}

	var parallel, serial []int
	for i, call := range calls {
		if parallelSafeCall(call) {
			parallel = append(parallel, i)
		} else {
			serial = append(serial, i)
		}
	}
	// Read-only calls can run together even when the same assistant turn also
	// contains a side-effecting call (for example a shell write).
	if len(parallel) > 1 {
		runParallel(parallel)
	} else {
		for _, i := range parallel {
			runOne(i)
		}
	}
	for _, i := range serial {
		runOne(i)
	}
	if batchID != "" {
		r.publish("tools.batch.completed", "", map[string]any{"agent_id": r.cfg.AgentID, "batch_id": batchID, "count": len(calls)})
	}
	return out
}

func parallelSafeCall(call domain.ToolCall) bool {
	switch call.Name {
	case "read_file", "list_dir", "glob", "grep", "git", "web_fetch", "web_search", "list_tasks", "list_workers":
		return true
	case "shell":
		return shellReadOnly(call.Arguments)
	default:
		return false
	}
}

func shellReadOnly(raw string) bool {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(raw), &in) != nil {
		return false
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return false
	}
	command = strings.ReplaceAll(command, "2>/dev/null", "")
	command = strings.ReplaceAll(command, "2>&1", "")
	if strings.Contains(command, ">") || strings.Contains(command, "<") ||
		strings.Contains(command, "`") || strings.Contains(command, "$(") {
		return false
	}
	// Split simple command chains and validate every segment. Pipes are fine
	// only when every stage is a known read-only command.
	normalized := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n").Replace(command)
	for _, segment := range strings.Split(normalized, "\n") {
		fields := strings.Fields(strings.TrimSpace(segment))
		if len(fields) == 0 {
			continue
		}
		name := filepath.Base(fields[0])
		switch name {
		case "cd", "pwd", "ls", "cat", "head", "tail", "grep", "egrep", "rg", "find", "wc", "sort", "uniq", "cut", "awk", "jq", "stat", "file", "readlink", "realpath", "tree", "du", "df", "test", "true", "false":
		case "sed":
			if strings.Contains(" "+strings.Join(fields[1:], " ")+" ", " -i ") || strings.Contains(" "+strings.Join(fields[1:], " ")+" ", " --in-place") {
				return false
			}
		case "git":
			if len(fields) < 2 {
				return false
			}
			switch fields[1] {
			case "status", "log", "show", "diff", "rev-parse", "ls-tree", "ls-files", "branch", "merge-base", "grep", "blame", "describe", "tag":
			default:
				return false
			}
		default:
			return false
		}
		joined := " " + strings.Join(fields[1:], " ") + " "
		if name == "find" && (strings.Contains(joined, " -delete ") || strings.Contains(joined, " -exec ") || strings.Contains(joined, " -execdir ")) {
			return false
		}
		if name == "awk" && strings.Contains(joined, "system(") {
			return false
		}
	}
	return true
}

func (r *Runtime) runTool(ctx context.Context, call domain.ToolCall) (string, error) {
	t, ok := r.cfg.Tools.Get(call.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", call.Name)
	}
	if !json.Valid([]byte(call.Arguments)) {
		return "", fmt.Errorf("invalid JSON arguments")
	}
	env := r.cfg.Permissions
	env.Mode = r.cfg.Mode
	env.Cwd = r.cfg.Cwd
	env.RunID = r.cfg.RunID
	env.TaskID = r.cfg.TaskID
	env.AgentID = r.cfg.AgentID
	env.SessionID = r.cfg.SessionID
	timeout := toolTimeout(call.Name)
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type outcome struct {
		text string
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				ch <- outcome{err: fmt.Errorf("tool %s panicked: %v", call.Name, rec)}
			}
		}()
		text, err := t.Run(toolCtx, env, json.RawMessage(call.Arguments))
		ch <- outcome{text: text, err: err}
	}()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case out := <-ch:
			return out.text, out.err
		case <-ticker.C:
			r.publish("tool.running", "", map[string]any{"agent_id": r.cfg.AgentID, "name": call.Name, "elapsed_seconds": int(time.Since(start).Seconds())})
		case <-toolCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("tool %s timed out after %s; narrow the request or add filters", call.Name, timeout)
		}
	}
}

func toolTimeout(name string) time.Duration {
	switch name {
	case "grep":
		return 60 * time.Second
	case "glob", "list_dir", "read_file", "web_fetch", "web_search", "git":
		return 30 * time.Second
	case "shell", "shell_background":
		return 10 * time.Minute
	default:
		return 120 * time.Second
	}
}

func (r *Runtime) publish(typ, text string, payload any) {
	ev := domain.Event{
		Time: time.Now(), SessionID: r.cfg.SessionID, RunID: r.cfg.RunID,
		TaskID: r.cfg.TaskID, AgentID: r.cfg.AgentID,
		Type: typ, Text: text, Payload: payload,
	}
	if r.cfg.Events != nil {
		r.cfg.Events.Publish(ev)
	}
	if r.cfg.OnEvent != nil {
		r.cfg.OnEvent(ev)
	}
}
