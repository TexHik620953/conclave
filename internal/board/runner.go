package board

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/agent"
	"github.com/texhik/conclave/internal/conclave"
	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/gitx"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/mcp"
	"github.com/texhik/conclave/internal/prompt"
	"github.com/texhik/conclave/internal/questions"
	"github.com/texhik/conclave/internal/tools"
	"github.com/texhik/conclave/internal/tracker"
	"github.com/texhik/conclave/internal/usage"
)

type ClientFactory func(ref string) (llm.Client, llm.Model, error)

type Deps struct {
	Config    *config.Config
	Profile   config.Profile
	Tracker   *tracker.Service
	Bus       *event.Bus
	Cwd       string
	Client    ClientFactory
	Usage     *usage.Service
	Questions *questions.Service
	MCP       *mcp.Manager
}

type Status struct {
	Running bool           `json:"running"`
	Active  int            `json:"active"`
	Workers map[string]int `json:"workers"`
}

type Runner struct {
	deps      Deps
	mu        sync.Mutex
	run       bool
	active    map[string]int
	providers map[string]int
	cancel    context.CancelFunc
}

func New(d Deps) *Runner {
	return &Runner{deps: d, active: map[string]int{}, providers: map[string]int{}}
}

func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.run {
		r.mu.Unlock()
		return fmt.Errorf("queue is already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.run = true
	r.cancel = cancel
	r.mu.Unlock()
	r.deps.Tracker.SetQueueRunning(context.Background(), true)
	r.publish("queue.changed", "queue started")
	go r.loop(runCtx)
	return nil
}

func (r *Runner) Pause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	r.run = false
	r.deps.Tracker.SetQueueRunning(context.Background(), false)
	r.publish("queue.changed", "queue paused")
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := map[string]int{}
	for k, v := range r.active {
		w[k] = v
	}
	return Status{Running: r.run, Active: sum(w), Workers: w}
}

func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run
}

func (r *Runner) loop(ctx context.Context) {
	defer func() {
		r.mu.Lock()
		r.run = false
		r.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		if r.tryDispatch(ctx) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *Runner) tryDispatch(ctx context.Context) bool {
	for _, w := range r.deps.Profile.Workers {
		if !r.hasSlot(w) {
			continue
		}
		slots := []tracker.WorkerSlot{{ID: w.ID, Complexities: w.Complexity, Labels: nil}}
		task, err := r.deps.Tracker.ClaimNext(ctx, w.ID, slots, 15*time.Minute)
		if err != nil {
			continue
		}
		if task == nil {
			continue
		}
		r.addActive(w, task.ID)
		r.publish("queue.changed", "task claimed: "+task.ID)
		go r.execute(ctx, *task, w)
		return true
	}
	return false
}

func (r *Runner) hasSlot(w config.WorkerConfig) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := w.Instances
	if limit <= 0 {
		limit = 1
	}
	if r.active[w.ID] >= limit {
		return false
	}
	max := r.deps.Profile.Limits.MaxParallel
	if max > 0 && sum(r.active) >= max {
		return false
	}
	_, model, err := r.deps.Client(w.Model)
	if err != nil {
		return false
	}
	providerLimit := r.deps.Config.Providers[model.Provider].MaxParallel
	if v, ok := r.deps.Profile.Limits.Providers[model.Provider]; ok && v > 0 {
		providerLimit = v
	}
	if providerLimit <= 0 {
		providerLimit = max
	}
	if providerLimit > 0 && r.providers[model.Provider] >= providerLimit {
		return false
	}
	return true
}

func (r *Runner) addActive(w config.WorkerConfig, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[w.ID]++
	if _, model, err := r.deps.Client(w.Model); err == nil {
		r.providers[model.Provider]++
	}
}

func (r *Runner) removeActive(w config.WorkerConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[w.ID] > 0 {
		r.active[w.ID]--
	}
	if _, model, err := r.deps.Client(w.Model); err == nil && r.providers[model.Provider] > 0 {
		r.providers[model.Provider]--
	}
}

func (r *Runner) execute(ctx context.Context, task tracker.Task, w config.WorkerConfig) {
	defer func() {
		r.removeActive(w)
		r.publish("queue.changed", "task finished: "+task.ID)
	}()
	if _, err := r.deps.Tracker.Transition(ctx, task.ID, tracker.StatusInProgress, w.ID); err != nil {
		return
	}
	short := strings.ReplaceAll(task.ID, "-", "")
	agentID := "board-" + short
	client, model, err := r.deps.Client(w.Model)
	if err != nil {
		r.fail(ctx, task.ID, w.ID, err)
		return
	}
	repoDir, err := gitx.ResolveRepo(ctx, r.deps.Cwd, append(append([]string{}, task.WriteScope...), task.Component, task.Title, task.Description)...)
	if err != nil {
		r.fail(ctx, task.ID, w.ID, err)
		return
	}
	repo := gitx.Repo{Dir: repoDir}
	boardDir := filepath.Join(r.deps.Cwd, ".conclave", "board", filepath.Base(repoDir))
	if err := os.MkdirAll(boardDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(boardDir, ".gitignore"), []byte("*\n"), 0o644)
	}
	initial := domain.Message{
		ID: domain.NewID("msg"), Role: domain.RoleUser, CreatedAt: time.Now(),
		Content: "Execute this project task. Inspect the worktree, implement the change, verify it, and leave the result committed.",
	}
	runID := "board_" + short
	var base, branch, dir string
	var history []domain.Message
	checkpoint, resumed := loadTaskCheckpoint(boardDir, task.ID)
	if resumed {
		dir, branch, base = checkpoint.Dir, checkpoint.Branch, checkpoint.Base
		history = repairCheckpointMessages(checkpoint.Messages)
		if len(history) == 0 {
			history = []domain.Message{initial}
		}
		r.deps.Tracker.AddComment(context.Background(), task.ID, "queue", "resuming interrupted attempt in "+dir)
		r.publish("board.changed", "task resumed: "+task.ID)
	} else if oldDir, oldBranch, ok := findExistingWorktree(ctx, boardDir, task.ID); ok {
		// Worktrees created before checkpoint support are still useful: keep
		// their files and ask the agent to continue instead of starting over.
		dir, branch = oldDir, oldBranch
		if head, herr := (gitx.Repo{Dir: dir}).Head(ctx); herr == nil {
			base = head
		}
		history = []domain.Message{initial, {
			ID: domain.NewID("msg"), Role: domain.RoleUser, CreatedAt: time.Now(),
			Content: "This task was interrupted. Continue from the existing worktree state; inspect what is already done and finish the remaining work without starting over.",
		}}
		resumed = true
		r.deps.Tracker.AddComment(context.Background(), task.ID, "queue", "resuming existing worktree "+dir)
		r.publish("board.changed", "task resumed: "+task.ID)
	} else {
		base, err = repo.Snapshot(ctx)
		if err != nil {
			r.fail(ctx, task.ID, w.ID, err)
			return
		}
		branch = fmt.Sprintf("conclave/board/%s-a%d", task.ID, task.Attempts)
		dir = filepath.Join(boardDir, fmt.Sprintf("%s-a%d", task.ID, task.Attempts))
		if err := repo.WorktreeAdd(ctx, dir, branch, base); err != nil {
			r.fail(ctx, task.ID, w.ID, err)
			return
		}
		history = []domain.Message{initial}
	}
	taskDomain := domain.Task{ID: task.ID, Title: task.Title, Spec: task.Description, Acceptance: task.Acceptance, WriteScope: task.WriteScope, WorkerID: w.ID, Model: w.Model}
	system := prompt.WorkerSystem(r.deps.Config, taskDomain, w, dir)
	if task.Context != "" {
		system += "\n\nTask context:\n" + task.Context
	}
	attempt, err := r.deps.Tracker.StartAttempt(ctx, task.ID, task.Attempts, agentID, w.ID, model.Ref)
	if err != nil {
		r.fail(ctx, task.ID, w.ID, err)
		return
	}
	r.publish("board.changed", "task started: "+task.ID)
	cp := &taskCheckpoint{TaskID: task.ID, RepoDir: repoDir, Branch: branch, Dir: dir, Base: base, Messages: append([]domain.Message(nil), history...)}
	saveTaskCheckpoint(boardDir, cp)
	rt := agent.New(agent.Config{
		AgentID: agentID, SessionID: "board", RunID: runID, TaskID: task.ID,
		Model: model, Client: client, System: system, Effort: w.Effort, MaxSteps: w.MaxSteps,
		Cwd: dir, Mode: domain.ModeBuild, Tools: boardTools(r.deps.Tracker, r.deps.Questions, r.deps.Config.Search, r.deps.MCP, w), Events: r.deps.Bus,
		Permissions: tools.Env{Cwd: dir, Mode: domain.ModeBuild, Permissions: r.deps.Config.Permissions},
		OnEvent: func(ev domain.Event) {
			r.logEvent(attempt.ID, task.ID, ev)
		},
		OnMessage: func(msg domain.Message) {
			cp.Messages = append(cp.Messages, msg)
			saveTaskCheckpoint(boardDir, cp)
		},
	})
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeat := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-heartbeat:
				return
			case <-t.C:
				_ = r.deps.Tracker.Heartbeat(taskCtx, task.ID, w.ID, 15*time.Minute)
			case <-taskCtx.Done():
				return
			}
		}
	}()
	res, runErr := rt.Run(taskCtx, history)
	close(heartbeat)
	if r.deps.Usage != nil {
		for _, rec := range res.Records {
			_ = r.deps.Usage.Add(context.Background(), usage.Record{
				SessionID: task.OriginSessionID, TaskID: task.ID, AttemptID: attempt.ID, AgentID: agentID,
				Provider: rec.Provider, Model: rec.Model, Source: "worker",
				InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens,
				CachedTokens: rec.CachedTokens, ReasoningTokens: rec.ReasoningTokens,
				Requests: rec.Requests, LastContext: rec.LastContext, ContextWindow: rec.ContextWindow,
			})
		}
	}
	if runErr != nil {
		attemptStatus := "failed"
		if ctx.Err() != nil {
			attemptStatus = "cancelled"
		}
		r.deps.Tracker.FinishAttempt(context.Background(), attempt.ID, attemptStatus, "", "")
		if ctx.Err() != nil {
			_, _ = r.deps.Tracker.Transition(context.Background(), task.ID, tracker.StatusReady, "queue")
			_, _ = r.deps.Tracker.AddComment(context.Background(), task.ID, "queue", "cancelled")
		} else {
			r.fail(ctx, task.ID, w.ID, runErr)
		}
		return
	}
	if bt, gerr := r.deps.Tracker.Get(ctx, task.ID); gerr == nil && (bt.Status == tracker.StatusNeedsPlanning || bt.Status == tracker.StatusBlocked) {
		r.deps.Tracker.FinishAttempt(context.Background(), attempt.ID, string(bt.Status), branch, "")
		r.publish("board.changed", "task waiting: "+task.ID)
		return
	}
	commit, err := (gitx.Repo{Dir: dir}).CommitAll(ctx, "board: "+task.ID+" "+task.Title)
	if err != nil {
		r.deps.Tracker.FinishAttempt(context.Background(), attempt.ID, "failed", branch, "")
		r.fail(ctx, task.ID, w.ID, err)
		return
	}
	_ = r.deps.Tracker.SetExecution(ctx, task.ID, branch, commit, runID)
	r.deps.Tracker.FinishAttempt(context.Background(), attempt.ID, "review", branch, commit)
	summary := lastAssistant(res.Messages)
	if summary == "" {
		summary = "implemented"
	}
	_, _ = r.deps.Tracker.AddComment(ctx, task.ID, w.ID, summary)
	_, err = r.deps.Tracker.Transition(ctx, task.ID, tracker.StatusReview, w.ID)
	if err != nil {
		r.fail(ctx, task.ID, w.ID, err)
		return
	}
	removeTaskCheckpoint(boardDir, task.ID)
	r.publish("board.changed", "task ready for review: "+task.ID)
}

func (r *Runner) logEvent(attemptID, taskID string, ev domain.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch ev.Type {
	case "assistant.completed":
		if msg, ok := ev.Payload.(domain.Message); ok && strings.TrimSpace(msg.Content) != "" {
			r.deps.Tracker.Log(ctx, taskID, attemptID, "info", "assistant", msg.Content)
		}
	case "tool.started":
		if p, ok := ev.Payload.(map[string]any); ok {
			if call, ok := p["call"].(domain.ToolCall); ok {
				r.deps.Tracker.Log(ctx, taskID, attemptID, "info", "tool_call", call.Name+" "+call.Arguments)
			}
		}
	case "tool.completed":
		r.deps.Tracker.Log(ctx, taskID, attemptID, "info", "tool_result", ev.Text)
	case "tool.failed", "agent.error":
		r.deps.Tracker.Log(ctx, taskID, attemptID, "error", "error", ev.Text)
	}
}

const maxTaskRetries = 3

func (r *Runner) fail(ctx context.Context, id, actor string, err error) {
	task, _ := r.deps.Tracker.Get(context.Background(), id)
	attempts := 1
	if task != nil && task.Attempts > 0 {
		attempts = task.Attempts
	}
	if attempts <= maxTaskRetries {
		_, _ = r.deps.Tracker.AddComment(context.Background(), id, actor,
			fmt.Sprintf("attempt %d/%d failed: %s; retrying from checkpoint", attempts, maxTaskRetries+1, err.Error()))
		_, _ = r.deps.Tracker.Transition(context.Background(), id, tracker.StatusReady, actor)
		r.publish("board.changed", fmt.Sprintf("task retry %d/%d: %s", attempts, maxTaskRetries+1, id))
		return
	}
	reason := fmt.Sprintf("failed after %d attempts: %s", attempts, err.Error())
	_, _ = r.deps.Tracker.AddComment(context.Background(), id, actor, reason)
	_ = r.deps.Tracker.FailToBacklog(context.Background(), id, actor, reason)
	r.publish("board.changed", "task failed: "+id+" "+err.Error())
}

func (r *Runner) publish(typ, text string) {
	if r.deps.Bus != nil {
		r.deps.Bus.Publish(domain.Event{Time: time.Now(), Type: typ, Text: text})
	}
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func boardTools(tr *tracker.Service, qsvc *questions.Service, searchCfg config.SearchConfig, mcpMgr *mcp.Manager, w config.WorkerConfig) *tools.Registry {
	reg := tools.Builtins()
	if len(w.Tools) > 0 {
		reg = reg.Only(w.Tools...)
	}
	tools.AddWebTools(reg, searchCfg)
	if mcpMgr != nil {
		mcpMgr.AddTo(reg)
	}
	for _, tool := range conclave.TaskTools(tr) {
		reg.Add(tool)
	}
	reg.Add(requestDecompositionTool{tr: tr})
	if qsvc != nil {
		reg.Add(questions.AskTool{Svc: qsvc, Autonomous: true})
	}
	return reg
}

func lastAssistant(msgs []domain.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == domain.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}
