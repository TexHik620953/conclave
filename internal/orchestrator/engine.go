// Package orchestrator executes declarative multi-role pipelines.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/mcp"
	"github.com/texhik/conclave/internal/memory"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/role"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tool"
)

// Engine runs pipelines.
type Engine struct {
	Cfg      *config.Config
	LLM      *llm.Client
	Store    *store.Store
	Bus      *event.Bus
	Defaults role.Defaults
	MCP      *mcp.Manager
	Asker    tool.Asker
	Out      io.Writer
	// Inbox drains user messages queued while a run is active. Roles inject
	// them between iterations.
	Inbox func(runID string) []string

	mu       sync.RWMutex
	sessions map[string]string // runID -> sessionID
}

// setSession associates a run with a chat session.
func (e *Engine) setSession(runID, sessionID string) {
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	if e.sessions == nil {
		e.sessions = map[string]string{}
	}
	e.sessions[runID] = sessionID
	e.mu.Unlock()
}

func (e *Engine) clearSession(runID string) {
	e.mu.Lock()
	delete(e.sessions, runID)
	e.mu.Unlock()
}

func (e *Engine) sessionFor(runID string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessions[runID]
}

// inboxFor returns a drain function for the run's queued user messages.
func (e *Engine) inboxFor(runID string) func() []string {
	if e.Inbox == nil {
		return nil
	}
	return func() []string { return e.Inbox(runID) }
}

// RunOptions configures a single pipeline run.
type RunOptions struct {
	Pipeline    string
	SessionID   string
	Task        string
	Workspace   string
	Inputs      map[string]string
	ResumeRunID string
	RunID       string
	Followup    string
	OnDelta     func(provider.Delta)
}

// RunResult summarizes a completed run.
type RunResult struct {
	RunID     string
	Status    string
	Error     string
	Outputs   map[string]string
	Artifacts map[string]string
	Todos     []tool.Todo
	Usage     provider.Usage
	CostUSD   float64
}

type runContext struct {
	pipe    config.Pipeline
	state   *State
	nodes   map[string]config.Node
	edges   map[string][]config.Edge
	order   []string
	start   string
	opts    RunOptions
	onDelta func(provider.Delta)
}

// Run executes the named pipeline.
func (e *Engine) Run(ctx context.Context, opts RunOptions) (*RunResult, error) {
	var resumeRun *store.Run
	if opts.ResumeRunID != "" {
		if e.Store == nil {
			return nil, fmt.Errorf("resume requires a store")
		}
		run, err := e.Store.GetRun(opts.ResumeRunID)
		if err != nil {
			return nil, fmt.Errorf("resume: %w", err)
		}
		resumeRun = run
		if opts.Pipeline == "" {
			opts.Pipeline = run.Pipeline
		}
		if opts.Task == "" {
			opts.Task = run.Task
		}
		if opts.Workspace == "" {
			opts.Workspace = run.Workspace
		}
		if opts.SessionID == "" {
			opts.SessionID = run.SessionID
		}
	}

	// The controller decides what to do; a pipeline is optional. When none is
	// given we fall back to the configured default (the NN-driven supervisor).
	if opts.Pipeline == "" {
		opts.Pipeline = e.Cfg.Settings.DefaultPipeline
		if opts.Pipeline == "" {
			opts.Pipeline = "auto"
		}
	}
	pipe, ok := e.Cfg.Pipelines[opts.Pipeline]
	if !ok {
		return nil, fmt.Errorf("unknown pipeline %q", opts.Pipeline)
	}
	workspace := opts.Workspace
	if workspace == "" {
		workspace = pipe.Settings.Workspace
	}
	if workspace == "" {
		workspace = e.Cfg.Settings.Workspace
	}
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}

	rc, err := newRunContext(pipe, workspace, opts)
	if err != nil {
		return nil, err
	}

	done := map[string]bool{}
	if resumeRun != nil {
		rc.state.RunID = resumeRun.ID
		if err := e.restoreState(rc, resumeRun); err != nil {
			return nil, err
		}
		if opts.Followup != "" {
			rc.state.Task = strings.TrimSpace(rc.state.Task) + "\n\n## Follow-up\n" + opts.Followup
			// Keep the previous outputs as context but re-run every node so the
			// whole pipeline reacts to the new instruction.
			rc.state.ClearCompleted()
		}
		done = rc.state.completedNodes()
		if e.Store != nil {
			_ = e.Store.MarkRunning(resumeRun.ID)
		}
		e.emit(resumeRun.ID, "", "", event.RunStarted, "resumed "+resumeRun.Pipeline, nil)
	} else {
		if opts.RunID != "" {
			rc.state.RunID = opts.RunID
		} else {
			rc.state.RunID = NewRunID(opts.Pipeline)
		}
		rc.state.Task = opts.Task
		for k, v := range opts.Inputs {
			rc.state.Inputs[k] = v
		}
		if e.Store != nil {
			if err := e.Store.CreateRun(store.Run{
				ID: rc.state.RunID, SessionID: opts.SessionID, Pipeline: opts.Pipeline,
				Task: opts.Task, Workspace: workspace,
				Inputs: rc.state.Inputs, StartedAt: time.Now(),
			}); err != nil {
				return nil, err
			}
		}
		e.emit(rc.state.RunID, "", "", event.RunStarted, opts.Pipeline, nil)
	}
	runID := rc.state.RunID
	e.setSession(runID, opts.SessionID)
	defer e.clearSession(runID)
	ctx = tool.WithRunID(ctx, runID)

	stopHeartbeat := e.startHeartbeat(runID)
	defer stopHeartbeat()

	runErr := e.drive(ctx, rc, rc.start, done)

	status := "completed"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = "aborted"
		}
		errMsg = runErr.Error()
	}
	if e.Store != nil {
		_ = e.Store.UpdateRunUsage(runID, rc.state.Cost(), rc.state.Usage.PromptTokens, rc.state.Usage.CompletionTokens)
		_ = e.Store.FinishRun(runID, status, errMsg)
	}
	e.emit(runID, "", "", event.RunFinished, status, nil)

	outputs, artifacts := rc.state.Snapshot()
	res := &RunResult{
		RunID:     runID,
		Status:    status,
		Error:     errMsg,
		Outputs:   outputs,
		Artifacts: artifacts,
		Todos:     rc.state.Todos(),
		Usage:     rc.state.Usage,
		CostUSD:   rc.state.Cost(),
	}
	if runErr != nil {
		return res, runErr
	}
	return res, nil
}

// startHeartbeat periodically refreshes the run's liveness marker so another
// process opening the store does not mark an active run as interrupted. It
// returns a stop function.
func (e *Engine) startHeartbeat(runID string) func() {
	if e.Store == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = e.Store.Heartbeat(runID)
			}
		}
	}()
	return func() { close(done) }
}

// restoreState reloads outputs, artifacts and todos from a previous run.
func (e *Engine) restoreState(rc *runContext, run *store.Run) error {
	rc.state.Task = run.Task
	if len(run.Inputs) > 0 {
		rc.state.Inputs = map[string]string{}
		for k, v := range run.Inputs {
			rc.state.Inputs[k] = v
		}
	}
	rc.state.AddUsage(provider.Usage{PromptTokens: run.PromptTokens, CompletionTokens: run.CompletionTokens})
	rc.state.AddCost(run.CostUSD)

	nodes, err := e.Store.ListNodes(run.ID)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.Status != "completed" {
			continue
		}
		rc.state.SetOutput(n.NodeID, n.Output)
		rc.state.MarkCompleted(n.NodeID)
	}
	arts, err := e.Store.ListArtifacts(run.ID)
	if err != nil {
		return err
	}
	for _, art := range arts {
		data, err := os.ReadFile(art.Path)
		if err != nil {
			continue
		}
		rc.state.SetArtifact(art.Name, string(data))
	}
	if raw := rc.state.Artifact("todos.json"); raw != "" {
		var todos []tool.Todo
		if json.Unmarshal([]byte(raw), &todos) == nil {
			_ = rc.state.SetTodos(todos)
		}
	}
	return nil
}

// drive executes the pipeline graph. Nodes become ready when all their
// predecessors have completed or been pruned, and ready nodes run concurrently
// up to max_parallel.
func (e *Engine) drive(ctx context.Context, rc *runContext, start string, done map[string]bool) error {
	pending := map[string]int{}
	selectedIn := map[string]bool{}
	processed := map[string]bool{}
	for id := range rc.nodes {
		pending[id] = 0
	}
	for _, edges := range rc.edges {
		for _, ed := range edges {
			pending[ed.To]++
		}
	}
	if _, ok := rc.nodes[start]; !ok {
		return fmt.Errorf("unknown start node %q", start)
	}

	// Seed already-completed nodes (resume) so their successors are released.
	var ready []string
	for _, id := range rc.order {
		if !done[id] || processed[id] {
			continue
		}
		sel, err := e.resolveNext(rc, id)
		if err != nil {
			return err
		}
		selected := map[string]bool{}
		for _, t := range sel {
			selected[t] = true
		}
		e.propagate(rc, id, selected, pending, selectedIn, processed, &ready)
	}
	ready = dedupeReady(ready, done, processed)
	if !done[start] {
		pending[start] = 0
		selectedIn[start] = true
		ready = appendUnique(ready, start)
	}

	max := rc.pipe.Settings.MaxParallel
	if max <= 0 {
		max = e.Cfg.Settings.MaxParallel
	}
	if max <= 0 {
		max = 4
	}

	for len(ready) > 0 {
		sort.Strings(ready)
		results, err := e.runWave(ctx, rc, ready, max)
		if err != nil {
			return err
		}
		wave := make([]string, 0, len(results))
		for id := range results {
			wave = append(wave, id)
		}
		sort.Strings(wave)
		ready = nil
		for _, id := range wave {
			selected := map[string]bool{}
			for _, t := range results[id] {
				selected[t] = true
			}
			e.propagate(rc, id, selected, pending, selectedIn, processed, &ready)
		}
	}
	return nil
}

// runWave executes a set of ready nodes concurrently. If any node fails, the
// remaining nodes in the wave are cancelled so they do not keep making LLM
// calls or writing artifacts after a failure.
func (e *Engine) runWave(ctx context.Context, rc *runContext, ids []string, max int) (map[string][]string, error) {
	waveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(map[string][]string, len(ids))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-waveCtx.Done():
				return
			}
			node, ok := rc.nodes[id]
			if !ok {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("unknown node %q", id)
					cancel()
				}
				mu.Unlock()
				return
			}
			next, err := e.execNode(waveCtx, rc, node)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			results[id] = next
		}(id)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// propagate decrements successor counters after a node completes. Successors
// not selected by the completed node are pruned recursively so that joins do
// not deadlock.
func (e *Engine) propagate(rc *runContext, id string, selected map[string]bool, pending map[string]int, selectedIn map[string]bool, processed map[string]bool, ready *[]string) {
	if processed[id] {
		return
	}
	processed[id] = true
	for _, ed := range rc.edges[id] {
		t := ed.To
		pending[t]--
		if selected[t] {
			selectedIn[t] = true
		}
		if pending[t] == 0 && !processed[t] {
			if selectedIn[t] {
				*ready = append(*ready, t)
			} else {
				e.propagate(rc, t, map[string]bool{}, pending, selectedIn, processed, ready)
			}
		}
	}
}

func (e *Engine) execNode(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	switch node.Type {
	case "agent":
		return e.execAgent(ctx, rc, node)
	case "parallel":
		return e.execParallel(ctx, rc, node)
	case "controller":
		return e.execController(ctx, rc, node)
	case "supervisor":
		return e.execSupervisor(ctx, rc, node)
	case "gate":
		return e.execGate(ctx, rc, node)
	case "transform":
		return e.execTransform(ctx, rc, node)
	default:
		return nil, fmt.Errorf("node %s: unknown type %q", node.ID, node.Type)
	}
}

func (e *Engine) execAgent(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	roleID := node.Role
	if roleID == "" {
		roleID = node.ID
	}
	rt, err := e.buildRuntime(rc, roleID, &node)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	prompt := node.Prompt
	if prompt == "" {
		prompt = fmt.Sprintf("Act as %s and perform the %q step. Produce your output.", rt.Def.Title, node.ID)
	}
	ctxBlock := e.contextBlock(rc)
	e.emit(rc.state.RunID, node.ID, roleID, event.NodeStarted, node.Type, map[string]any{
		"model":   rt.Model,
		"role":    roleID,
		"prompt":  prompt,
		"context": capEventText(ctxBlock, 64*1024),
		"tools":   rt.Tools.Names(),
	})
	res, err := rt.Run(ctx, role.Input{Prompt: prompt, Context: ctxBlock})
	if err != nil {
		e.saveNode(rc, node.ID, roleID, "failed", prompt, "", err.Error(), started, 0)
		return nil, err
	}
	rc.state.SetOutput(node.ID, res.Content)
	rc.state.AddUsage(res.Usage)
	e.saveArtifact(rc, node, res.Content)
	e.saveNode(rc, node.ID, roleID, "completed", prompt, res.Content, "", started, res.Usage.TotalTokens)
	e.emit(rc.state.RunID, node.ID, roleID, event.NodeFinished, "completed", map[string]any{"tokens": res.Usage.TotalTokens, "model": rt.Model})
	return e.resolveNext(rc, node.ID)
}

func (e *Engine) execParallel(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	roles := append([]string{}, node.Roles...)
	if node.Role != "" {
		roles = append(roles, node.Role)
	}
	if len(roles) == 0 {
		return nil, fmt.Errorf("node %s: parallel requires roles", node.ID)
	}
	max := rc.pipe.Settings.MaxParallel
	if max <= 0 {
		max = e.Cfg.Settings.MaxParallel
	}
	if max <= 0 {
		max = 4
	}
	started := time.Now()
	e.emit(rc.state.RunID, node.ID, "", event.NodeStarted, "parallel", map[string]any{
		"roles":   roles,
		"prompt":  node.Prompt,
		"context": capEventText(e.contextBlock(rc), 64*1024),
	})

	ctxBlock := e.contextBlock(rc)
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := map[string]string{}
	errs := map[string]string{}
	totalTokens := 0

	for _, roleID := range roles {
		wg.Add(1)
		go func(roleID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rt, err := e.buildRuntime(rc, roleID, &config.Node{ID: node.ID})
			if err != nil {
				mu.Lock()
				errs[roleID] = err.Error()
				mu.Unlock()
				return
			}
			prompt := node.Prompt
			if prompt == "" {
				prompt = fmt.Sprintf("Act as %s and review the current work. End with a verdict line: STATUS: PASS or STATUS: FAIL.", rt.Def.Title)
			}
			res, err := rt.Run(ctx, role.Input{Prompt: prompt, Context: ctxBlock})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[roleID] = err.Error()
				return
			}
			results[roleID] = res.Content
			totalTokens += res.Usage.TotalTokens
			rc.state.AddUsage(res.Usage)
		}(roleID)
	}
	wg.Wait()

	var b strings.Builder
	fmt.Fprintf(&b, "# Parallel step: %s\n", node.ID)
	for _, roleID := range roles {
		fmt.Fprintf(&b, "\n## %s\n", roleID)
		if errMsg, ok := errs[roleID]; ok {
			fmt.Fprintf(&b, "ERROR: %s\n", errMsg)
			continue
		}
		content := results[roleID]
		rc.state.SetOutput(roleID, content)
		b.WriteString(content)
		b.WriteString("\n")
	}
	aggregate := b.String()
	rc.state.SetOutput(node.ID, aggregate)
	e.saveArtifact(rc, node, aggregate)
	e.saveNode(rc, node.ID, strings.Join(roles, ","), "completed", node.Prompt, aggregate, "", started, totalTokens)
	e.emit(rc.state.RunID, node.ID, "", event.NodeFinished, "completed", map[string]any{"roles": roles})

	if len(errs) > 0 && len(results) == 0 {
		return nil, fmt.Errorf("node %s: all parallel roles failed", node.ID)
	}
	return e.resolveNext(rc, node.ID)
}

func (e *Engine) execController(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	roleID := node.Role
	if roleID == "" {
		roleID = e.Cfg.Settings.Controller
	}
	if roleID == "" {
		roleID = "controller"
	}
	if _, ok := e.Cfg.Roles[roleID]; !ok {
		return nil, fmt.Errorf("node %s: controller role %q not found", node.ID, roleID)
	}
	if len(node.Choices) == 0 {
		return nil, fmt.Errorf("node %s: controller requires choices", node.ID)
	}
	rt, err := e.buildRuntime(rc, roleID, &node)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	prompt := buildControllerPrompt(node, rc.state)
	ctxBlock := e.contextBlock(rc)
	e.emit(rc.state.RunID, node.ID, roleID, event.NodeStarted, "controller", map[string]any{
		"model":   rt.Model,
		"role":    roleID,
		"prompt":  prompt,
		"context": capEventText(ctxBlock, 64*1024),
		"tools":   rt.Tools.Names(),
	})

	res, err := rt.Run(ctx, role.Input{Prompt: prompt, Context: ctxBlock})
	if err != nil {
		e.saveNode(rc, node.ID, roleID, "failed", prompt, "", err.Error(), started, 0)
		return nil, err
	}
	choice, err := parseChoice(res.Content, node.Choices)
	if err != nil {
		e.saveNode(rc, node.ID, roleID, "failed", prompt, res.Content, err.Error(), started, res.Usage.TotalTokens)
		return nil, err
	}
	rc.state.SetOutput(node.ID, res.Content)
	rc.state.AddUsage(res.Usage)
	e.saveArtifact(rc, node, res.Content)
	e.saveNode(rc, node.ID, roleID, "completed", prompt, res.Content, "", started, res.Usage.TotalTokens)
	e.emit(rc.state.RunID, node.ID, roleID, event.NodeFinished, choice, map[string]any{"model": rt.Model})
	return []string{choice}, nil
}

func (e *Engine) execGate(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	ok, err := Eval(node.Condition, rc.state, rc.state.CurrentIterations())
	if err != nil {
		return nil, fmt.Errorf("node %s: condition: %w", node.ID, err)
	}
	if ok && node.Ask {
		switch e.askApproval(ctx, rc, node) {
		case gateApprove:
			ok = true
		case gateReject:
			ok = false
		case gateStop:
			e.emit(rc.state.RunID, node.ID, "", event.NodeFinished,
				fmt.Sprintf("gate %s stopped", node.ID), map[string]any{"passed": false, "stopped": true})
			return nil, nil
		}
	}
	e.emit(rc.state.RunID, node.ID, "", event.NodeFinished,
		fmt.Sprintf("gate %s = %v", node.ID, ok), map[string]any{"passed": ok})
	if ok {
		return e.resolveNext(rc, node.ID)
	}
	if node.Else != "" {
		return []string{node.Else}, nil
	}
	return nil, nil
}

type gateDecision int

const (
	gateReject gateDecision = iota
	gateApprove
	gateStop
)

// askApproval asks the user to approve a gate. When no interactive user is
// available it falls back to the configured on_no_user policy.
func (e *Engine) askApproval(ctx context.Context, rc *runContext, node config.Node) gateDecision {
	question := node.Prompt
	if question == "" {
		question = fmt.Sprintf("Approve to continue past gate %q?", node.ID)
	}
	if e.Asker == nil {
		return e.noUserDecision(rc, node)
	}
	ans, err := e.Asker.Ask(ctx, tool.Question{
		Header:      "approval required",
		Question:    question,
		Options:     []tool.Option{{Label: "Approve"}, {Label: "Reject"}},
		AllowCustom: false,
	})
	if err != nil {
		if errors.Is(err, tool.ErrNoInteractiveUser) {
			return e.noUserDecision(rc, node)
		}
		e.emit(rc.state.RunID, node.ID, "", event.Error,
			fmt.Sprintf("gate %s: %v", node.ID, err), nil)
		return gateReject
	}
	for _, s := range ans.Selected {
		if strings.EqualFold(s, "Approve") {
			return gateApprove
		}
	}
	return gateReject
}

func (e *Engine) noUserDecision(rc *runContext, node config.Node) gateDecision {
	switch e.Cfg.Settings.OnNoUser {
	case "else":
		e.emit(rc.state.RunID, node.ID, "", event.Error,
			fmt.Sprintf("gate %s: no interactive user; on_no_user=else", node.ID), nil)
		return gateReject
	case "stop":
		e.emit(rc.state.RunID, node.ID, "", event.Error,
			fmt.Sprintf("gate %s: no interactive user; on_no_user=stop", node.ID), nil)
		return gateStop
	default:
		e.emit(rc.state.RunID, node.ID, "", event.Error,
			fmt.Sprintf("gate %s: no interactive user; defaulting to next", node.ID), nil)
		return gateApprove
	}
}

func (e *Engine) execTransform(ctx context.Context, rc *runContext, node config.Node) ([]string, error) {
	content := renderTemplate(node.Template, rc.state)
	rc.state.SetOutput(node.ID, content)
	e.saveArtifact(rc, node, content)
	return e.resolveNext(rc, node.ID)
}

// buildRuntime assembles a role runtime honouring node-level overrides.
func (e *Engine) buildRuntime(rc *runContext, roleID string, node *config.Node) (*role.Runtime, error) {
	def, ok := e.Cfg.Roles[roleID]
	if !ok {
		return nil, fmt.Errorf("unknown role %q", roleID)
	}
	env := role.BuildEnv(def, rc.state.Workspace, e.Defaults)
	env.Todos = &stateTodoStore{state: rc.state, onUpdate: func(todos []tool.Todo) {
		e.onTodosUpdated(rc, todos)
	}}
	env.Asker = e.Asker
	registry := tool.NewRegistry(env)
	if e.MCP != nil {
		for _, t := range e.MCP.Tools() {
			registry.Register(t)
		}
	}
	filtered, err := registry.Filter(effectiveTools(def, node))
	if err != nil {
		return nil, fmt.Errorf("role %s: %w", roleID, err)
	}
	model := def.Model
	fallback := def.Fallback
	if node != nil && node.Model != "" {
		model = node.Model
	}
	nodeID := ""
	if node != nil {
		nodeID = node.ID
	}
	sumThreshold := 0.0
	if e.Cfg.Settings.SummarizeThreshold != nil {
		sumThreshold = *e.Cfg.Settings.SummarizeThreshold
	}
	return &role.Runtime{
		Def:                def,
		LLM:                e.LLM,
		Tools:              filtered,
		MaxIterations:      e.Cfg.Settings.MaxIterations,
		Bus:                e.Bus,
		RunID:              rc.state.RunID,
		NodeID:             nodeID,
		Model:              model,
		Fallback:           fallback,
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
	}, nil
}

func (e *Engine) recordCost(rc *runContext, providerName, model string, usage provider.Usage) {
	ref := providerName + "/" + model
	rc.state.AddCost(e.Cfg.EstimateCost(ref, usage.PromptTokens, usage.CompletionTokens))
}

func (e *Engine) checkBudget(rc *runContext) error {
	budget := e.Cfg.Settings.BudgetUSD
	if budget <= 0 {
		return nil
	}
	cost := rc.state.Cost()
	if cost < budget {
		return nil
	}
	if e.Cfg.Settings.OnBudget == "abort" {
		return fmt.Errorf("budget exceeded: $%.4f >= $%.4f", cost, budget)
	}
	if rc.state.MarkBudgetWarned() {
		e.emit(rc.state.RunID, "", "", event.Error,
			fmt.Sprintf("budget exceeded: $%.4f >= $%.4f (continuing)", cost, budget), nil)
	}
	return nil
}

type stateTodoStore struct {
	state    *State
	onUpdate func([]tool.Todo)
}

func (s *stateTodoStore) Todos() []tool.Todo { return s.state.Todos() }

func (s *stateTodoStore) SetTodos(todos []tool.Todo) error {
	if err := s.state.SetTodos(todos); err != nil {
		return err
	}
	if s.onUpdate != nil {
		s.onUpdate(todos)
	}
	return nil
}

func (e *Engine) onTodosUpdated(rc *runContext, todos []tool.Todo) {
	e.emit(rc.state.RunID, "", "", event.TodosUpdated, tool.RenderTodos(todos),
		map[string]any{"todos": todos})
	if e.Store != nil {
		if data, err := json.MarshalIndent(todos, "", "  "); err == nil {
			_, _ = e.Store.SaveArtifact(rc.state.RunID, "todos.json", string(data))
		}
	}
}

func (e *Engine) contextBlock(rc *runContext) string {
	return memory.TrimContext(rc.state.ContextBlock(), e.Cfg.Settings.MaxContext)
}

func capEventText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}

func effectiveTools(def config.Role, node *config.Node) []string {
	if node != nil && len(node.Tools) > 0 {
		return node.Tools
	}
	return def.Tools
}

func (e *Engine) saveArtifact(rc *runContext, node config.Node, content string) {
	name := node.Output
	if name == "" {
		name = node.ID + ".md"
	}
	rc.state.SetArtifact(name, content)
	if e.Store != nil {
		_, _ = e.Store.SaveArtifact(rc.state.RunID, name, content)
	}
}

func (e *Engine) saveNode(rc *runContext, nodeID, roleID, status, prompt, output, errMsg string, started time.Time, tokens int) {
	if e.Store == nil {
		return
	}
	_ = e.Store.SaveNode(store.NodeRecord{
		RunID: rc.state.RunID, NodeID: nodeID, Role: roleID, Status: status,
		Prompt: prompt, Output: output, Error: errMsg, StartedAt: started, FinishedAt: time.Now(), Tokens: tokens,
	})
}

func (e *Engine) emit(runID, nodeID, roleID string, typ event.Type, message string, data map[string]any) {
	e.emitEvent(event.Event{Type: typ, RunID: runID, NodeID: nodeID, Role: roleID, Message: message, Data: data})
}

// EmitEvent lets external components (e.g. the question broker) publish an
// event through the same persistence path as the engine.
func (e *Engine) EmitEvent(ev event.Event) { e.emitEvent(ev) }

// emitEvent publishes an event to the bus and persists it so the run timeline
// survives a reload.
func (e *Engine) emitEvent(ev event.Event) {
	if e.Bus != nil {
		e.Bus.Publish(ev)
	}
	if e.Store != nil {
		_ = e.Store.AppendEvent(ev.RunID, string(ev.Type), ev.NodeID, ev.Role, ev.Message, ev.Data)
		e.recordMessage(ev)
	}
}

// recordMessage appends conversational events to the run's session log so the
// chat can be replayed after a reload.
func (e *Engine) recordMessage(ev event.Event) {
	sid := e.sessionFor(ev.RunID)
	if sid == "" {
		return
	}
	kind, content, data := "", "", ev.Data
	switch ev.Type {
	case event.RoleMessage:
		kind, content = "text", ev.Message
	case event.ToolCall:
		kind = "tool_call"
	case event.ToolResult:
		kind = "tool_result"
	case event.UserQuestion:
		kind = "question"
	case event.Error:
		kind, content = "error", ev.Message
	default:
		return
	}
	_, _ = e.Store.AppendMessage(store.Message{
		SessionID: sid, RunID: ev.RunID, Role: ev.Role, Model: ev.Model,
		Kind: kind, Content: content, Data: data,
	})
}

func newRunContext(pipe config.Pipeline, workspace string, opts RunOptions) (*runContext, error) {
	nodes := map[string]config.Node{}
	order := make([]string, 0, len(pipe.Nodes))
	hasNext := false
	for _, n := range pipe.Nodes {
		nodes[n.ID] = n
		order = append(order, n.ID)
		if len(n.Next) > 0 {
			hasNext = true
		}
	}
	next := map[string][]config.Edge{}
	for _, e := range pipe.Edges {
		next[e.From] = appendEdge(next[e.From], e)
	}
	for _, n := range pipe.Nodes {
		for _, to := range n.Next {
			next[n.ID] = appendEdge(next[n.ID], config.Edge{From: n.ID, To: to})
		}
	}
	if len(pipe.Edges) == 0 && !hasNext {
		for i := 0; i+1 < len(order); i++ {
			next[order[i]] = appendEdge(next[order[i]], config.Edge{From: order[i], To: order[i+1]})
		}
	}
	// Implicit edges for dynamic nodes so the scheduler can track them.
	for _, n := range pipe.Nodes {
		if n.Type == "controller" {
			for _, c := range n.Choices {
				next[n.ID] = appendEdge(next[n.ID], config.Edge{From: n.ID, To: c})
			}
		}
		if n.Type == "gate" && n.Else != "" {
			next[n.ID] = appendEdge(next[n.ID], config.Edge{From: n.ID, To: n.Else})
		}
	}
	start := pipe.Start
	if start == "" && len(order) > 0 {
		start = order[0]
	}
	return &runContext{
		pipe: pipe, state: NewState("", "", workspace), nodes: nodes, edges: next,
		order: order, start: start, opts: opts, onDelta: opts.OnDelta,
	}, nil
}

func appendEdge(list []config.Edge, e config.Edge) []config.Edge {
	for _, x := range list {
		if x.To == e.To && x.When == e.When {
			return list
		}
	}
	return append(list, e)
}

// resolveNext returns the successors of a node whose edge conditions hold.
func (e *Engine) resolveNext(rc *runContext, nodeID string) ([]string, error) {
	var out []string
	for _, ed := range rc.edges[nodeID] {
		if ed.When == "" {
			out = appendUnique(out, ed.To)
			continue
		}
		ok, err := Eval(ed.When, rc.state, rc.state.CurrentIterations())
		if err != nil {
			return nil, fmt.Errorf("edge %s -> %s: %w", nodeID, ed.To, err)
		}
		if ok {
			out = appendUnique(out, ed.To)
		}
	}
	return out, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func dedupeReady(in []string, done, processed map[string]bool) []string {
	var out []string
	for _, id := range in {
		if done[id] || processed[id] {
			continue
		}
		out = appendUnique(out, id)
	}
	return out
}

func buildControllerPrompt(node config.Node, st *State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the pipeline controller at decision node %q.\n", node.ID)
	if node.Prompt != "" {
		b.WriteString(node.Prompt)
		b.WriteString("\n")
	}
	b.WriteString("\nChoose exactly one of the following next steps:\n")
	for _, c := range node.Choices {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	b.WriteString("\nRespond with only the chosen step id, nothing else.\n")
	return b.String()
}

func parseChoice(content string, choices []string) (string, error) {
	trimmed := strings.TrimSpace(content)
	for _, c := range choices {
		if trimmed == c {
			return c, nil
		}
	}
	lower := strings.ToLower(trimmed)
	sorted := append([]string{}, choices...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, c := range sorted {
		if strings.Contains(lower, strings.ToLower(c)) {
			return c, nil
		}
	}
	return "", fmt.Errorf("controller did not choose a valid option %v; got %q", choices, trimmed)
}

// NewRunID returns a unique run identifier for a pipeline.
func NewRunID(pipeline string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	stamp := time.Now().UTC().Format("20060102-150405")
	return fmt.Sprintf("%s-%s-%s", pipeline, stamp, hex.EncodeToString(buf))
}
