package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/role"
	"github.com/texhik/conclave/internal/tool"
)

// toolSchema builds a JSON-schema object for a tool's parameters.
func toolSchema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

// execSupervisor runs a controller-driven loop: the controller LLM decides
// which roles to run next via the delegate/delegate_parallel tools and ends the
// step with finish. It complements the static node types rather than replacing
// them.
func (e *Engine) execSupervisor(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	roleID := node.Role
	if roleID == "" {
		roleID = e.Cfg.Settings.Controller
	}
	if roleID == "" {
		roleID = "controller"
	}
	def, ok := e.Cfg.Roles[roleID]
	if !ok {
		return nil, fmt.Errorf("node %s: supervisor role %q not found", node.ID, roleID)
	}

	allowed := append([]string{}, node.Roles...)
	if len(allowed) == 0 {
		for id := range e.Cfg.Roles {
			if id != roleID {
				allowed = append(allowed, id)
			}
		}
		sort.Strings(allowed)
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("node %s: supervisor has no roles to delegate to", node.ID)
	}

	maxSteps := node.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 8
	}

	var steps int
	finished := false
	var summary string

	env := role.BuildEnv(def, rc.state.Workspace, e.Defaults)
	env.Todos = &stateTodoStore{state: rc.state, onUpdate: func(todos []tool.Todo) {
		e.onTodosUpdated(rc, todos)
	}}
	env.Asker = e.Asker
	reg := tool.NewRegistry(env)
	if e.MCP != nil {
		for _, t := range e.MCP.Tools() {
			reg.Register(t)
		}
	}
	reg.Register(&delegateTool{e: e, rc: rc, node: node, allowed: allowed, parallel: false, steps: &steps})
	reg.Register(&delegateTool{e: e, rc: rc, node: node, allowed: allowed, parallel: true, steps: &steps})
	reg.Register(&finishTool{finished: &finished, summary: &summary})

	filtered, err := reg.Filter([]string{"delegate", "delegate_parallel", "finish", "ask_user", "todo_write", "todo_read"})
	if err != nil {
		return nil, err
	}

	sumThreshold := 0.0
	if e.Cfg.Settings.SummarizeThreshold != nil {
		sumThreshold = *e.Cfg.Settings.SummarizeThreshold
	}
	model := def.Model
	if node.Model != "" {
		model = node.Model
	}
	rt := &role.Runtime{
		Def:                def,
		LLM:                e.LLM,
		Tools:              filtered,
		MaxIterations:      maxSteps,
		Bus:                e.Bus,
		RunID:              rc.state.RunID,
		NodeID:             node.ID,
		Model:              model,
		Fallback:           def.Fallback,
		ContextLimit:       e.Cfg.ContextLimit(model),
		Emit:               e.emitEvent,
		Inbox:              e.inboxFor(rc.state.RunID),
		OnDelta:            rc.onDelta,
		SummarizeThreshold: sumThreshold,
		SummarizerModel:    e.Cfg.Settings.SummarizerModel,
		BeforeCall: func() error {
			return e.checkBudget(rc)
		},
		AfterCall: func(prov, m string, usage provider.Usage) {
			e.recordCost(rc, prov, m, usage)
		},
	}

	started := time.Now()
	prompt := node.Prompt
	if prompt == "" {
		prompt = "Decide the next step and delegate work to the available roles. Call finish when the task is complete."
	}
	prompt += e.roleRoster(allowed)
	ctxBlock := e.contextBlock(rc)
	e.emit(rc.state.RunID, node.ID, roleID, event.NodeStarted, "supervisor", map[string]any{
		"model":   model,
		"role":    roleID,
		"prompt":  prompt,
		"context": capEventText(ctxBlock, 64*1024),
		"roles":   allowed,
		"tools":   filtered.Names(),
	})

	res, err := rt.Run(ctx, role.Input{Prompt: prompt, Context: ctxBlock})
	if err != nil {
		e.saveNode(rc, node.ID, roleID, "failed", prompt, "", err.Error(), started, 0)
		return nil, err
	}
	content := strings.TrimSpace(res.Content)
	if strings.TrimSpace(summary) != "" {
		content = summary
	}
	if content == "" {
		content = fmt.Sprintf("supervisor finished after %d delegation(s)", steps)
	}
	rc.state.SetOutput(node.ID, content)
	rc.state.AddUsage(res.Usage)
	e.saveArtifact(rc, node, content)
	e.saveNode(rc, node.ID, roleID, "completed", prompt, content, "", started, res.Usage.TotalTokens)
	e.emit(rc.state.RunID, node.ID, roleID, event.NodeFinished, "completed", map[string]any{
		"model": model, "steps": steps, "finished": finished,
	})
	return e.resolveNext(rc, node.ID)
}

// roleRoster renders the available roles for the supervisor's prompt so the
// controller knows the full pool it can delegate to.
func (e *Engine) roleRoster(allowed []string) string {
	var b strings.Builder
	b.WriteString("\n\nAvailable roles (use these exact ids with the delegate tools):\n")
	for _, id := range allowed {
		def, ok := e.Cfg.Roles[id]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- %s", id)
		if def.Title != "" {
			fmt.Fprintf(&b, " — %s", def.Title)
		}
		if def.Description != "" {
			fmt.Fprintf(&b, ": %s", def.Description)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// runDelegatedRole executes a role as a child step of a supervisor node.
func (e *Engine) runDelegatedRole(ctx context.Context, rc *runContext, node config.Node, roleID, prompt string, parallel bool) (string, error) {
	if _, ok := e.Cfg.Roles[roleID]; !ok {
		return "", fmt.Errorf("unknown role %q", roleID)
	}
	childID := fmt.Sprintf("%s.%s", node.ID, roleID)
	// Use the delegated node id so the role's own events (messages, tool calls)
	// are grouped under the child step rather than the supervisor node.
	child := config.Node{ID: childID, Role: roleID}
	rt, err := e.buildRuntime(rc, roleID, &child)
	if err != nil {
		return "", err
	}
	started := time.Now()
	e.emit(rc.state.RunID, childID, roleID, event.NodeStarted, "delegate", map[string]any{
		"model": rt.Model, "role": roleID, "prompt": prompt, "parallel": parallel,
	})
	res, err := rt.Run(ctx, role.Input{Prompt: prompt, Context: e.contextBlock(rc)})
	if err != nil {
		e.saveNode(rc, childID, roleID, "failed", prompt, "", err.Error(), started, 0)
		return "", err
	}
	rc.state.SetOutput(childID, res.Content)
	rc.state.AddUsage(res.Usage)
	e.saveArtifact(rc, config.Node{ID: childID, Output: childID + ".md"}, res.Content)
	e.saveNode(rc, childID, roleID, "completed", prompt, res.Content, "", started, res.Usage.TotalTokens)
	e.emit(rc.state.RunID, childID, roleID, event.NodeFinished, "completed", map[string]any{"model": rt.Model})
	return res.Content, nil
}

// delegateTool exposes role execution to the supervisor controller.
type delegateTool struct {
	e        *Engine
	rc       *runContext
	node     config.Node
	allowed  []string
	parallel bool
	steps    *int
}

func (t *delegateTool) Name() string {
	if t.parallel {
		return "delegate_parallel"
	}
	return "delegate"
}

func (t *delegateTool) Description() string {
	if t.parallel {
		return "Run several roles concurrently on the same prompt and return all results."
	}
	return "Run a single role as a sub-step and return its result."
}

func (t *delegateTool) Schema() json.RawMessage {
	if t.parallel {
		return toolSchema(map[string]any{
			"roles": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": t.allowed},
			},
			"prompt": map[string]any{"type": "string"},
		}, "roles", "prompt")
	}
	return toolSchema(map[string]any{
		"role":   map[string]any{"type": "string", "enum": t.allowed},
		"prompt": map[string]any{"type": "string"},
	}, "role", "prompt")
}

func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	*t.steps++
	// Expose the delegation count to conditions/templates as `iterations`.
	t.rc.state.SetIterations(*t.steps)
	if *t.steps > 100 {
		return tool.Result{Content: "delegation limit reached", IsError: true}, nil
	}
	if t.parallel {
		var in struct {
			Roles  []string `json:"roles"`
			Prompt string   `json:"prompt"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return tool.Result{}, err
		}
		if len(in.Roles) == 0 || strings.TrimSpace(in.Prompt) == "" {
			return tool.Result{Content: "roles and prompt are required", IsError: true}, nil
		}
		return t.runParallel(ctx, in.Roles, in.Prompt)
	}
	var in struct {
		Role   string `json:"role"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, err
	}
	if in.Role == "" || strings.TrimSpace(in.Prompt) == "" {
		return tool.Result{Content: "role and prompt are required", IsError: true}, nil
	}
	if !t.isAllowed(in.Role) {
		return tool.Result{Content: fmt.Sprintf("role %q is not available", in.Role), IsError: true}, nil
	}
	out, err := t.e.runDelegatedRole(ctx, t.rc, t.node, in.Role, in.Prompt, false)
	if err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: out}, nil
}

func (t *delegateTool) runParallel(ctx context.Context, roles []string, prompt string) (tool.Result, error) {
	for _, r := range roles {
		if !t.isAllowed(r) {
			return tool.Result{Content: fmt.Sprintf("role %q is not available", r), IsError: true}, nil
		}
	}
	max := t.e.Cfg.Settings.MaxParallel
	if max <= 0 {
		max = 4
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	var mu sync.Mutex
	out := map[string]string{}
	errs := map[string]string{}
	for _, r := range roles {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			content, err := t.e.runDelegatedRole(ctx, t.rc, t.node, r, prompt, true)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[r] = err.Error()
				return
			}
			out[r] = content
		}(r)
	}
	wg.Wait()

	var b strings.Builder
	for _, r := range roles {
		fmt.Fprintf(&b, "## %s\n", r)
		if msg, ok := errs[r]; ok {
			fmt.Fprintf(&b, "ERROR: %s\n", msg)
			continue
		}
		b.WriteString(out[r])
		b.WriteString("\n")
	}
	return tool.Result{Content: b.String()}, nil
}

func (t *delegateTool) isAllowed(roleID string) bool {
	for _, a := range t.allowed {
		if a == roleID {
			return true
		}
	}
	return false
}

// finishTool lets the supervisor end its loop with a summary.
type finishTool struct {
	finished *bool
	summary  *string
}

func (t *finishTool) Name() string { return "finish" }
func (t *finishTool) Description() string {
	return "Finish the supervisor step with a final summary of what was done."
}
func (t *finishTool) Schema() json.RawMessage {
	return toolSchema(map[string]any{"summary": map[string]any{"type": "string"}}, "summary")
}
func (t *finishTool) Execute(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	var in struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, err
	}
	*t.finished = true
	*t.summary = strings.TrimSpace(in.Summary)
	return tool.Result{Content: "supervisor finished"}, nil
}
