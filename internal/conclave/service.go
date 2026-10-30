package conclave

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/texhik/conclave/internal/agent"
	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/gitx"
	"github.com/texhik/conclave/internal/jobs"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/mcp"
	"github.com/texhik/conclave/internal/prompt"
	"github.com/texhik/conclave/internal/questions"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tools"
	"github.com/texhik/conclave/internal/tracker"
	"github.com/texhik/conclave/internal/usage"
)

type ClientFactory func(ref string) (llm.Client, llm.Model, error)

type Deps struct {
	Config     *config.Config
	Profile    config.Profile
	Store      *store.Store
	Bus        *event.Bus
	SessionID  string
	Cwd        string
	Client     ClientFactory
	MCP        *mcp.Manager
	Jobs       *jobs.Manager
	Tasks      *tracker.Service
	Usage      *usage.Service
	Questions  *questions.Service
	StartQueue func(context.Context) error
	PauseQueue func()
}

type Service struct {
	cfg        *config.Config
	profile    config.Profile
	store      *store.Store
	bus        *event.Bus
	sessionID  string
	cwd        string
	client     ClientFactory
	mcp        *mcp.Manager
	jobs       *jobs.Manager
	tasks      *tracker.Service
	usage      *usage.Service
	questions  *questions.Service
	startQueue func(context.Context) error
	pauseQueue func()

	mu          sync.Mutex
	run         *domain.Run
	pendingPlan *domain.Plan
	messages    []domain.Message
	busy        bool
	running     bool
	finishFlag  bool
	newPool     bool
	lastSummary string
	cancel      context.CancelFunc
	runIDValue  atomic.Value
}

type Snapshot struct {
	Run           *domain.Run      `json:"run"`
	PendingPlan   *domain.Plan     `json:"pending_plan,omitempty"`
	Messages      []domain.Message `json:"messages,omitempty"`
	Busy          bool             `json:"busy"`
	Running       bool             `json:"running"`
	MasterModel   string           `json:"master_model"`
	MasterEffort  string           `json:"master_effort"`
	MasterContext int              `json:"master_context"`
}

func New(deps Deps) *Service {
	master := deps.Profile.Master.Model
	if master == "" {
		master = deps.Profile.Agent.Model
	}
	s := &Service{
		cfg: deps.Config, profile: deps.Profile, store: deps.Store, bus: deps.Bus,
		sessionID: deps.SessionID, cwd: deps.Cwd, client: deps.Client, mcp: deps.MCP, jobs: deps.Jobs, tasks: deps.Tasks, usage: deps.Usage, questions: deps.Questions, startQueue: deps.StartQueue, pauseQueue: deps.PauseQueue,
		run: &domain.Run{
			ID: domain.NewID("run"), SessionID: deps.SessionID,
			Phase: domain.RunPlanning, Iteration: 0,
			MaxIterations: deps.Profile.Limits.MaxIterations,
			CreatedAt:     time.Now(), UpdatedAt: time.Now(),
		},
	}
	if runs, err := deps.Store.ListRuns(context.Background(), deps.SessionID, 20); err == nil {
		for _, candidate := range runs {
			// Ignore empty runs left behind by a previous server start.
			if len(candidate.Tasks) == 0 && candidate.Plan == nil && candidate.Goal == "" && candidate.Usage.Requests == 0 {
				continue
			}
			s.run = candidate
			break
		}
	}
	if s.run.MaxIterations <= 0 {
		s.run.MaxIterations = 6
	}
	if s.run.Phase == domain.RunApproval {
		if s.run.Plan == nil {
			s.run.Plan = s.recoverPendingPlan(s.run.ID)
		}
		if s.run.Plan != nil {
			s.pendingPlan = s.run.Plan
		}
	}
	if s.run.Phase == domain.RunRunning || s.run.Phase == domain.RunReviewing {
		s.run.Phase = domain.RunPaused
		s.run.Error = "server restarted; resume the run to continue"
	}
	s.runIDValue.Store(s.run.ID)
	if sess, err := deps.Store.LoadSession(context.Background(), deps.SessionID); err == nil && sess != nil {
		s.messages = append([]domain.Message(nil), sess.ConclaveMessages...)
	}
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": s.run.Phase, "run_id": s.run.ID})
	return s
}

// recoverPendingPlan rebuilds the approval preview from Board tasks created by
// submit_plan before the plan itself was persisted in the run record.
func (s *Service) recoverPendingPlan(runID string) *domain.Plan {
	if s.tasks == nil || runID == "" {
		return nil
	}
	tasks, err := s.tasks.List(context.Background(), tracker.Filter{Limit: 1000})
	if err != nil {
		return nil
	}
	plan := &domain.Plan{Goal: s.run.Goal}
	for _, bt := range tasks {
		if bt.Context != "Conclave run "+runID {
			continue
		}
		if bt.Status != tracker.StatusBacklog && bt.Status != tracker.StatusReady {
			continue
		}
		plan.Tasks = append(plan.Tasks, domain.Task{
			ID: bt.ID, BoardID: bt.ID, Title: bt.Title, Spec: bt.Description,
			Complexity: bt.Complexity, Priority: bt.Priority, Acceptance: bt.Acceptance,
			DependsOn: bt.DependsOn, WriteScope: bt.WriteScope, Kind: bt.Kind,
			Status: domain.TaskPending,
		})
	}
	if len(plan.Tasks) == 0 {
		return nil
	}
	return plan
}

func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy || s.running
}

func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Service) recordUsage(rec domain.UsageRecord, agentID string) {
	if s.usage == nil {
		return
	}
	source := "worker"
	if agentID == "master" {
		source = "master"
	} else if agentID == "master-review" {
		source = "reviewer"
	}
	_ = s.usage.Add(context.Background(), usage.Record{
		SessionID: s.sessionID, TaskID: rec.TaskID, AgentID: agentID,
		Provider: rec.Provider, Model: rec.Model, Source: source,
		InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens,
		CachedTokens: rec.CachedTokens, ReasoningTokens: rec.ReasoningTokens,
		Requests: rec.Requests, LastContext: rec.LastContext, ContextWindow: rec.ContextWindow,
	})
}

func (s *Service) persistMessagesLocked() {
	if s.store == nil {
		return
	}
	sess, err := s.store.LoadSession(context.Background(), s.sessionID)
	if err != nil || sess == nil {
		return
	}
	sess.ConclaveMessages = append([]domain.Message(nil), s.messages...)
	_ = s.store.SaveSession(context.Background(), sess)
}

func (s *Service) persistMessages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistMessagesLocked()
}

func (s *Service) Revert(keep int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keep < 0 {
		keep = 0
	}
	if keep > len(s.messages) {
		keep = len(s.messages)
	}
	s.messages = append([]domain.Message(nil), s.messages[:keep]...)
	s.persistMessagesLocked()
}

func (s *Service) RegenerateAt(ctx context.Context, index int) error {
	s.mu.Lock()
	if index < 0 {
		for i := len(s.messages) - 1; i >= 0; i-- {
			if s.messages[i].Role == domain.RoleUser {
				index = i
				break
			}
		}
	}
	if index < 0 || index >= len(s.messages) {
		s.mu.Unlock()
		return fmt.Errorf("no message to regenerate")
	}
	s.messages = append([]domain.Message(nil), s.messages[:index]...)
	s.persistMessagesLocked()
	s.mu.Unlock()
	return s.Send(ctx, "Regenerate the last response from the current context. Do not restart completed work.")
}

func (s *Service) Regenerate(ctx context.Context) error {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return fmt.Errorf("conclave is busy")
	}
	idx := -1
	var prompt string
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == domain.RoleUser {
			idx = i
			prompt = s.messages[i].Content
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no user message to regenerate")
	}
	s.messages = append([]domain.Message(nil), s.messages[:idx]...)
	s.persistMessagesLocked()
	s.mu.Unlock()
	return s.Send(ctx, prompt)
}

func (s *Service) MasterModel() string {
	if s.profile.Master.Model != "" {
		return s.profile.Master.Model
	}
	return s.profile.Agent.Model
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := cloneRun(s.run)
	var plan *domain.Plan
	if s.pendingPlan != nil {
		p := *s.pendingPlan
		plan = &p
	}
	msgs := append([]domain.Message(nil), s.messages...)
	effort := s.profile.Master.Effort
	if effort == "" {
		effort = "high"
	}
	contextLimit := 0
	if m, err := s.cfg.ResolveModel(s.MasterModel()); err == nil {
		contextLimit = m.Config.Context
	}
	return Snapshot{Run: run, PendingPlan: plan, Messages: msgs, Busy: s.busy, Running: s.running, MasterModel: s.MasterModel(), MasterEffort: effort, MasterContext: contextLimit}
}

func (s *Service) Send(ctx context.Context, text string) error {
	s.mu.Lock()
	if s.busy || s.running {
		s.mu.Unlock()
		return fmt.Errorf("conclave is busy; wait for the current step or use /pause")
	}
	if s.run.Phase == domain.RunCompleted || s.run.Phase == domain.RunCancelled || s.run.Phase == domain.RunFailed {
		newRun := &domain.Run{
			ID: domain.NewID("run"), SessionID: s.sessionID, Phase: domain.RunPlanning,
			MaxIterations: s.profile.Limits.MaxIterations, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if newRun.MaxIterations <= 0 {
			newRun.MaxIterations = 6
		}
		s.run = newRun
		s.runIDValue.Store(newRun.ID)
		s.messages = nil
		s.pendingPlan = nil
	}
	s.busy = true
	s.messages = append(s.messages, domain.Message{ID: domain.NewID("msg"), Role: domain.RoleUser, Content: text, CreatedAt: time.Now()})
	history := append([]domain.Message(nil), s.messages...)
	s.persistMessagesLocked()
	masterRef := s.MasterModel()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	client, model, err := s.client(masterRef)
	if err != nil {
		return err
	}
	reg := tools.ReadOnly()
	tools.AddWebSearch(reg, s.cfg.Search)
	reg.Add(submitPlanTool{svc: s})
	if s.tasks != nil {
		reg.Add(createTaskTool{tr: s.tasks})
		reg.Add(listTasksTool{tr: s.tasks})
		reg.Add(commentTaskTool{tr: s.tasks})
	}
	reg.Add(listWorkersTool{profile: s.profile})
	if s.questions != nil {
		reg.Add(questions.AskTool{Svc: s.questions})
	}
	effort := s.profile.Master.Effort
	if effort == "" {
		effort = "high"
	}
	system := prompt.Build(s.cfg, s.profile, domain.ModeConclave, s.cwd,
		"Call submit_plan when the task DAG is ready. The user must approve it before workers start.")
	streamed := false
	rt := agent.New(agent.Config{
		AgentID: "master", SessionID: s.sessionID, RunID: s.runID(),
		Model: model, Client: client, System: system, Effort: effort, MaxSteps: s.profile.Master.MaxSteps,
		Cwd: s.cwd, Mode: domain.ModePlan, Tools: reg, Events: s.bus,
		Permissions: tools.Env{Cwd: s.cwd, Mode: domain.ModePlan, Permissions: s.cfg.Permissions},
		OnMessage: func(msg domain.Message) {
			streamed = true
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.persistMessagesLocked()
			s.mu.Unlock()
		},
	})
	res, err := rt.Run(ctx, history)
	s.mu.Lock()
	if !streamed {
		s.messages = append(s.messages, res.Messages...)
	}
	s.run.Usage.Add(res.Usage)
	for _, rec := range res.Records {
		s.run.Ledger.Add(rec)
		s.recordUsage(rec, rec.AgentID)
	}
	if s.pendingPlan != nil && s.run.Phase == domain.RunPlanning {
		s.run.Phase = domain.RunApproval
	}
	s.mu.Unlock()
	s.persistMessages()
	s.save()
	if err != nil {
		if ctx.Err() == nil {
			s.publish("run.error", err.Error(), nil)
		}
		return err
	}
	s.publish("run.phase_changed", "", map[string]any{"phase": s.runPhase(), "run_id": s.runID()})
	return nil
}

func (s *Service) Approve(ctx context.Context) error {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return fmt.Errorf("master is still finishing the plan; wait for it to stop before approving")
	}
	if s.run == nil || s.pendingPlan == nil {
		s.mu.Unlock()
		return fmt.Errorf("no plan to approve; ask the master to produce one")
	}
	if s.run.Phase != domain.RunApproval {
		s.mu.Unlock()
		return fmt.Errorf("run is not awaiting approval (phase: %s)", s.run.Phase)
	}
	plan := *s.pendingPlan
	s.pendingPlan = nil
	s.run.Plan = &plan
	s.run.Goal = plan.Goal
	s.run.Tasks = append([]domain.Task(nil), plan.Tasks...)
	normalizeTasks(s.run.Tasks)
	s.run.Phase = domain.RunRunning
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()
	s.save()
	s.publish("plan.approved", "", plan)
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunRunning, "run_id": s.run.ID})
	if s.tasks != nil && s.startQueue != nil {
		ids := make([]string, 0, len(plan.Tasks))
		for _, t := range plan.Tasks {
			if t.BoardID != "" {
				ids = append(ids, t.BoardID)
			}
		}
		if err := s.tasks.AssignBatch(ctx, ids, s.run.ID); err != nil {
			s.fail("assign batch: " + err.Error())
			return err
		}
		_ = s.startQueue(ctx)
		go s.watchBatch(runCtx)
		return nil
	}
	go s.execute(runCtx)
	return nil
}

func (s *Service) Pause() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return fmt.Errorf("run is not active")
	}
	cancel := s.cancel
	pauseQueue := s.pauseQueue
	runID := s.run.ID
	s.run.Phase = domain.RunPaused
	s.running = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pauseQueue != nil {
		pauseQueue()
	}
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunPaused, "run_id": runID})
	return nil
}

func (s *Service) Resume(ctx context.Context) error {
	s.mu.Lock()
	if s.run == nil || s.run.Phase != domain.RunPaused {
		s.mu.Unlock()
		return fmt.Errorf("run is not paused")
	}
	for i := range s.run.Tasks {
		if s.run.Tasks[i].Status == domain.TaskRunning || s.run.Tasks[i].Status == domain.TaskQueued {
			s.run.Tasks[i].Status = domain.TaskPending
		}
	}
	s.run.Phase = domain.RunRunning
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()
	s.save()
	if s.tasks != nil && s.startQueue != nil {
		var ids []string
		for _, t := range s.run.Tasks {
			if t.BoardID == "" {
				continue
			}
			bt, err := s.tasks.Get(context.Background(), t.BoardID)
			if err == nil && bt.Status == tracker.StatusReady {
				ids = append(ids, t.BoardID)
			}
		}
		if len(ids) > 0 {
			_ = s.tasks.AssignBatch(context.Background(), ids, s.run.ID)
		}
		_ = s.startQueue(context.Background())
		go s.watchBatch(runCtx)
		return nil
	}
	go s.execute(runCtx)
	return nil
}

// RetryReview restarts the master review after a failed run. It also adopts
// review tasks that were created outside the current batch by older workers.
func (s *Service) RetryReview(ctx context.Context) error {
	s.mu.Lock()
	if s.busy || s.running {
		s.mu.Unlock()
		return fmt.Errorf("conclave is busy")
	}
	if s.run == nil || len(s.run.Tasks) == 0 {
		s.mu.Unlock()
		return fmt.Errorf("run has no tasks to review")
	}
	if s.run.Phase != domain.RunFailed && s.run.Phase != domain.RunPaused {
		s.mu.Unlock()
		return fmt.Errorf("run is not failed or paused (phase: %s)", s.run.Phase)
	}
	s.mu.Unlock()
	s.adoptReviewTasks(ctx)
	s.mu.Lock()
	s.run.Phase = domain.RunRunning
	s.run.Error = ""
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunRunning, "run_id": s.runID()})
	go s.watchBatch(runCtx)
	return nil
}

func (s *Service) adoptReviewTasks(ctx context.Context) {
	if s.tasks == nil {
		return
	}
	review, err := s.tasks.List(ctx, tracker.Filter{Status: []tracker.TaskStatus{tracker.StatusReview}, Limit: 1000})
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return
	}
	known := map[string]bool{}
	for _, t := range s.run.Tasks {
		if t.BoardID != "" {
			known[t.BoardID] = true
		}
	}
	for _, bt := range review {
		if known[bt.ID] {
			continue
		}
		if err := s.tasks.SetBatchID(ctx, bt.ID, s.run.ID, s.run.ID); err != nil {
			continue
		}
		s.run.Tasks = append(s.run.Tasks, domain.Task{
			ID: bt.ID, BoardID: bt.ID, Title: bt.Title, Spec: bt.Description,
			Complexity: bt.Complexity, Priority: bt.Priority, Kind: bt.Kind,
			Status: domain.TaskReview,
		})
	}
}

func (s *Service) Cancel() error {
	s.mu.Lock()
	cancel := s.cancel
	pauseQueue := s.pauseQueue
	if s.run != nil {
		for i := range s.run.Tasks {
			if s.run.Tasks[i].Status == domain.TaskRunning || s.run.Tasks[i].Status == domain.TaskQueued || s.run.Tasks[i].Status == domain.TaskPending {
				s.run.Tasks[i].Status = domain.TaskCanceled
			}
		}
		s.run.Phase = domain.RunCancelled
	}
	s.running = false
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pauseQueue != nil {
		pauseQueue()
	}
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunCancelled})
	return nil
}

func (s *Service) Apply(ctx context.Context) error {
	s.mu.Lock()
	run := cloneRun(s.run)
	s.mu.Unlock()
	if run == nil || run.IntegrationBranch == "" {
		return fmt.Errorf("no integration branch for this run")
	}
	if run.Phase != domain.RunCompleted && run.Phase != domain.RunReviewing {
		return fmt.Errorf("run is not complete (phase: %s)", run.Phase)
	}
	repo := gitx.Repo{Dir: s.cwd}
	if err := repo.CheckoutMerge(ctx, run.IntegrationBranch); err != nil {
		return err
	}
	s.publish("run.applied", "Applied "+run.IntegrationBranch, nil)
	return nil
}

func (s *Service) submitPlan(ctx context.Context, plan domain.Plan) error {
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("plan has no tasks")
	}
	if err := s.validatePlan(&plan); err != nil {
		return err
	}
	if s.tasks != nil {
		boardIDs := map[string]string{}
		for i := range plan.Tasks {
			t := &plan.Tasks[i]
			if t.BoardID != "" {
				boardIDs[t.ID] = t.BoardID
				continue
			}
			complexity := t.Complexity
			if complexity == 0 {
				complexity = 3
			}
			priority := t.Priority
			if priority == 0 {
				priority = 3
			}
			bt, err := s.tasks.Create(ctx, tracker.CreateInput{
				Title: t.Title, Description: t.Spec, Context: "Conclave run " + s.runID(),
				Complexity: maxInt(complexity, 1), Priority: maxInt(priority, 1), Status: tracker.StatusBacklog,
				Component: "", Labels: nil, Epic: "",
				WriteScope: t.WriteScope, Acceptance: t.Acceptance,
				CreatedBy: "master",
			}, "master")
			if err != nil {
				return err
			}
			t.BoardID = bt.ID
			boardIDs[t.ID] = bt.ID
		}
		for i := range plan.Tasks {
			t := &plan.Tasks[i]
			var deps []string
			for _, dep := range t.DependsOn {
				if boardID := boardIDs[dep]; boardID != "" {
					deps = append(deps, boardID)
				}
			}
			if len(deps) > 0 && t.BoardID != "" {
				if err := s.tasks.SetDependencies(ctx, t.BoardID, deps, "master"); err != nil {
					return err
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return fmt.Errorf("run is not initialized")
	}
	switch s.run.Phase {
	case domain.RunPlanning, domain.RunApproval:
		s.pendingPlan = &plan
		s.run.Plan = &plan
		s.run.Phase = domain.RunApproval
		s.run.Goal = plan.Goal
	case domain.RunRunning, domain.RunReviewing:
		// Next pool: keep completed tasks, append new ones with pool-unique IDs.
		prefix := fmt.Sprintf("p%d-", s.run.Iteration+1)
		remap := map[string]string{}
		for i := range plan.Tasks {
			old := plan.Tasks[i].ID
			remap[old] = prefix + old
		}
		for i := range plan.Tasks {
			plan.Tasks[i].ID = remap[plan.Tasks[i].ID]
			for j := range plan.Tasks[i].DependsOn {
				if v, ok := remap[plan.Tasks[i].DependsOn[j]]; ok {
					plan.Tasks[i].DependsOn[j] = v
				}
			}
			plan.Tasks[i].Status = domain.TaskPending
		}
		normalizeTasks(plan.Tasks)
		s.run.Tasks = append(s.run.Tasks, plan.Tasks...)
		s.run.Plan = &plan
		s.run.Goal = plan.Goal
		s.run.Iteration++
		s.newPool = true
	default:
		return fmt.Errorf("cannot submit a plan in phase %s", s.run.Phase)
	}
	go s.save()
	s.publish("plan.ready", "", plan)
	return nil
}

func (s *Service) canFinish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || len(s.run.Tasks) == 0 {
		return fmt.Errorf("cannot finish: create Board tasks and complete them first")
	}
	researchGoal := false
	goal := strings.ToLower(s.run.Goal)
	for _, word := range []string{"research", "analy", "audit", "исслед", "изуч", "анализ", "аудит"} {
		if strings.Contains(goal, word) {
			researchGoal = true
			break
		}
	}
	hasResearch := false
	for _, t := range s.run.Tasks {
		if t.Kind == "research" {
			hasResearch = true
		}
		if t.Status == domain.TaskFailed || t.Status == domain.TaskCanceled {
			return fmt.Errorf("cannot finish: task %s is %s", t.ID, t.Status)
		}
		if t.Status == domain.TaskPending || t.Status == domain.TaskRunning || t.Status == domain.TaskQueued {
			return fmt.Errorf("cannot finish: task %s is still %s; approve/reject it first", t.ID, t.Status)
		}
	}
	if researchGoal && !hasResearch {
		return fmt.Errorf("research goal requires at least one research task assigned to the researcher profile; add research tasks before finish_run")
	}
	return nil
}

func (s *Service) finish(summary string) {
	s.mu.Lock()
	s.finishFlag = true
	s.lastSummary = summary
	s.mu.Unlock()
}

func (s *Service) validatePlan(plan *domain.Plan) error {
	ids := map[string]bool{}
	for i := range plan.Tasks {
		t := &plan.Tasks[i]
		if t.ID == "" {
			return fmt.Errorf("task %d has no id", i+1)
		}
		if ids[t.ID] {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		ids[t.ID] = true
		w, err := s.workerStrict(t.WorkerID)
		if err != nil {
			w = s.defaultWorker(t.Kind)
			if w.ID == "" {
				return fmt.Errorf("no worker profiles configured")
			}
			t.WorkerID = w.ID
		}
		t.Model = w.Model
		t.Status = domain.TaskPending
	}
	for _, t := range plan.Tasks {
		for _, dep := range t.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}
	if cycle := findCycle(plan.Tasks); cycle != "" {
		return fmt.Errorf("task dependency cycle: %s", cycle)
	}
	return nil
}

func findCycle(tasks []domain.Task) string {
	state := map[string]int{}
	byID := map[string]domain.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var visit func(string, string) string
	visit = func(id, path string) string {
		if state[id] == 1 {
			return path + " -> " + id
		}
		if state[id] == 2 {
			return ""
		}
		state[id] = 1
		for _, dep := range byID[id].DependsOn {
			if c := visit(dep, path+" -> "+id); c != "" {
				return c
			}
		}
		state[id] = 2
		return ""
	}
	for _, t := range tasks {
		if c := visit(t.ID, ""); c != "" {
			return c
		}
	}
	return ""
}

func (s *Service) watchBatch(ctx context.Context) {
	if s.tasks == nil {
		s.fail("task tracker unavailable")
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		runID := s.run.ID
		iteration := s.run.Iteration
		maxIter := s.run.MaxIterations
		s.mu.Unlock()
		tasks, err := s.tasks.ByBatch(ctx, runID)
		if err != nil {
			s.fail("read batch: " + err.Error())
			return
		}
		// Decomposed child tasks inherit the parent batch. Include them in the
		// run model so the master review sees their results and finish_run can
		// close them like regular pool tasks.
		s.mu.Lock()
		known := map[string]bool{}
		for _, t := range s.run.Tasks {
			if t.BoardID != "" {
				known[t.BoardID] = true
			}
		}
		for _, bt := range tasks {
			if known[bt.ID] {
				continue
			}
			s.run.Tasks = append(s.run.Tasks, domain.Task{
				ID: bt.ID, BoardID: bt.ID, Title: bt.Title, Spec: bt.Description,
				Complexity: bt.Complexity, Priority: bt.Priority, Kind: bt.Kind,
				Status: domain.TaskPending,
			})
		}
		s.mu.Unlock()
		terminal := len(tasks) > 0
		hasWork := false
		for _, t := range tasks {
			switch t.Status {
			case tracker.StatusReady, tracker.StatusClaimed, tracker.StatusInProgress:
				terminal = false
				hasWork = true
			}
		}
		if !terminal {
			_ = hasWork
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		// sync board results into the run
		s.mu.Lock()
		byBoard := map[string]tracker.Task{}
		for _, t := range tasks {
			byBoard[t.ID] = t
		}
		for i := range s.run.Tasks {
			bt, ok := byBoard[s.run.Tasks[i].BoardID]
			if !ok {
				continue
			}
			s.run.Tasks[i].Branch = bt.Branch
			s.run.Tasks[i].Commit = bt.Commit
			switch bt.Status {
			case tracker.StatusDone:
				s.run.Tasks[i].Status = domain.TaskDone
			case tracker.StatusReview:
				s.run.Tasks[i].Status = domain.TaskReview
			case tracker.StatusBlocked:
				s.run.Tasks[i].Status = domain.TaskFailed
			case tracker.StatusBacklog:
				if bt.FailureReason != "" {
					s.run.Tasks[i].Status = domain.TaskFailed
				}
			case tracker.StatusCancelled:
				s.run.Tasks[i].Status = domain.TaskCanceled
			}
		}
		s.mu.Unlock()
		if err := s.reviewWithMaster(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.fail("master review: " + err.Error())
			return
		}
		s.mu.Lock()
		finish := s.finishFlag
		newPool := s.newPool
		summary := s.lastSummary
		s.finishFlag = false
		s.newPool = false
		s.mu.Unlock()
		if finish {
			s.completeBatch("")
			return
		}
		if !newPool {
			s.completeBatch(summary)
			return
		}
		if iteration+1 >= maxIter {
			s.mu.Lock()
			s.run.Phase = domain.RunPaused
			s.run.Error = fmt.Sprintf("maximum iterations reached (%d)", maxIter)
			s.mu.Unlock()
			s.save()
			return
		}
		s.approveReviewTasks(context.Background())
		s.mu.Lock()
		s.run.Iteration++
		s.run.Phase = domain.RunRunning
		ids := make([]string, 0, len(s.run.Tasks))
		for _, t := range s.run.Tasks {
			if t.BoardID != "" && (t.Status == domain.TaskPending || t.Status == domain.TaskBlocked) {
				ids = append(ids, t.BoardID)
			}
		}
		s.mu.Unlock()
		_ = s.tasks.AssignBatch(context.Background(), ids, runID)
		_ = s.startQueue(context.Background())
	}
}

func (s *Service) approveReviewTasks(ctx context.Context) {
	s.mu.Lock()
	tasks := append([]domain.Task(nil), s.run.Tasks...)
	s.mu.Unlock()
	if s.tasks == nil {
		return
	}
	for i := range tasks {
		t := &tasks[i]
		if t.BoardID == "" || t.Status != domain.TaskReview {
			continue
		}
		if _, err := s.tasks.Transition(ctx, t.BoardID, tracker.StatusDone, "master-review"); err == nil {
			s.mu.Lock()
			for j := range s.run.Tasks {
				if s.run.Tasks[j].BoardID == t.BoardID {
					s.run.Tasks[j].Status = domain.TaskDone
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Service) completeBatch(summary string) {
	s.mu.Lock()
	s.run.Phase = domain.RunCompleted
	tasks := append([]domain.Task(nil), s.run.Tasks...)
	s.mu.Unlock()
	if s.tasks != nil {
		for _, t := range tasks {
			if t.BoardID == "" {
				continue
			}
			bt, err := s.tasks.Get(context.Background(), t.BoardID)
			if err == nil && bt.Status == tracker.StatusReview {
				_, _ = s.tasks.Transition(context.Background(), t.BoardID, tracker.StatusDone, "conclave")
			}
		}
	}
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunCompleted, "summary": summary})
}

func (s *Service) execute(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	repo := gitx.Repo{Dir: s.cwd}
	if !repo.IsRepo(ctx) {
		s.fail("Conclave requires a git repository")
		return
	}
	if err := s.prepareWorkspace(ctx, repo); err != nil {
		s.fail("prepare workspace: " + err.Error())
		return
	}
	for {
		s.mu.Lock()
		iteration := s.run.Iteration
		maxIter := s.run.MaxIterations
		s.mu.Unlock()
		if iteration >= maxIter {
			s.mu.Lock()
			s.run.Phase = domain.RunPaused
			s.run.Error = fmt.Sprintf("maximum iterations reached (%d)", maxIter)
			s.mu.Unlock()
			s.save()
			s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunPaused})
			return
		}
		if err := s.executePool(ctx, repo); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.fail(err.Error())
			return
		}
		if ctx.Err() != nil {
			return
		}
		if err := s.reviewWithMaster(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.fail("master review: " + err.Error())
			return
		}
		s.mu.Lock()
		finish := s.finishFlag
		newPool := s.newPool
		summary := s.lastSummary
		s.finishFlag = false
		s.newPool = false
		s.mu.Unlock()
		if finish {
			s.mu.Lock()
			s.run.Phase = domain.RunCompleted
			for _, t := range s.run.Tasks {
				if s.tasks != nil && t.BoardID != "" && t.Status == domain.TaskDone {
					_, _ = s.tasks.Transition(context.Background(), t.BoardID, tracker.StatusDone, "conclave")
				}
			}
			s.mu.Unlock()
			s.save()
			s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunCompleted, "summary": summary})
			return
		}
		if !newPool {
			s.mu.Lock()
			s.run.Phase = domain.RunCompleted
			for _, t := range s.run.Tasks {
				if s.tasks != nil && t.BoardID != "" && t.Status == domain.TaskDone {
					_, _ = s.tasks.Transition(context.Background(), t.BoardID, tracker.StatusDone, "conclave")
				}
			}
			s.mu.Unlock()
			s.save()
			s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunCompleted})
			return
		}
		s.mu.Lock()
		s.run.Iteration++
		s.run.Phase = domain.RunRunning
		s.mu.Unlock()
		s.save()
	}
}

func (s *Service) prepareWorkspace(ctx context.Context, repo gitx.Repo) error {
	s.mu.Lock()
	if s.run.IntegrationBranch != "" {
		s.mu.Unlock()
		return nil
	}
	run := cloneRun(s.run)
	s.mu.Unlock()
	runsDir := filepath.Join(s.cwd, ".conclave", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(runsDir, ".gitignore"), []byte("*\n"), 0o644)
	}
	base, err := repo.Snapshot(ctx)
	if err != nil {
		return err
	}
	short := strings.ReplaceAll(run.ID, "run_", "")
	if len(short) > 12 {
		short = short[:12]
	}
	runDir := filepath.Join(s.cwd, ".conclave", "runs", short)
	integrationDir := filepath.Join(runDir, "integration")
	branch := "conclave/run-" + short + "/integration"
	if err := repo.WorktreeAdd(ctx, integrationDir, branch, base); err != nil {
		return err
	}
	s.mu.Lock()
	s.run.BaseRef = base
	s.run.RunDir = runDir
	s.run.IntegrationDir = integrationDir
	s.run.IntegrationBranch = branch
	s.mu.Unlock()
	s.save()
	return nil
}

func (s *Service) executePool(ctx context.Context, repo gitx.Repo) error {
	adm := newAdmission(s.profile, s.cfg)
	s.mu.Lock()
	s.run.Phase = domain.RunRunning
	s.mu.Unlock()
	s.save()

	type outcome struct {
		index int
		task  domain.Task
		usage domain.Usage
		err   error
	}
	results := make(chan outcome, 64)
	inFlight := map[int]bool{}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dispatched := false
		s.mu.Lock()
		// Fail tasks whose dependencies failed.
		for i := range s.run.Tasks {
			t := &s.run.Tasks[i]
			if t.Status != domain.TaskPending && t.Status != domain.TaskBlocked {
				continue
			}
			blocked := false
			failed := false
			for _, dep := range t.DependsOn {
				d := findTask(s.run.Tasks, dep)
				if d == nil {
					failed = true
					break
				}
				if d.Status == domain.TaskFailed || d.Status == domain.TaskCanceled {
					failed = true
				}
				if d.Status != domain.TaskDone {
					blocked = true
				}
			}
			switch {
			case failed:
				t.Status = domain.TaskFailed
				t.Error = "a dependency failed"
			case blocked:
				t.Status = domain.TaskBlocked
			default:
				t.Status = domain.TaskQueued
			}
		}
		// Dispatch ready queued tasks.
		for i := range s.run.Tasks {
			t := &s.run.Tasks[i]
			if t.Status != domain.TaskQueued || inFlight[i] {
				continue
			}
			w, err := s.worker(t.WorkerID)
			if err != nil {
				t.Status = domain.TaskFailed
				t.Error = err.Error()
				continue
			}
			_, model, err := s.client(w.Model)
			if err != nil {
				t.Status = domain.TaskFailed
				t.Error = err.Error()
				continue
			}
			if !adm.tryAcquire(model.Provider, w.ID) {
				continue
			}
			t.Status = domain.TaskRunning
			t.Attempts++
			now := time.Now()
			t.StartedAt = &now
			t.LastAgentID = domain.NewID("worker")
			task := *t
			if s.tasks != nil && t.BoardID != "" {
				_ = s.tasks.StartTask(ctx, t.BoardID, w.ID)
			}
			agentID := t.LastAgentID
			inFlight[i] = true
			dispatched = true
			s.publish("task.started", "", map[string]any{"task": task, "agent_id": agentID})
			provider := model.Provider
			go func(idx int, task domain.Task, w config.WorkerConfig, agentID string) {
				out := s.runTask(ctx, repo, task, w, agentID)
				out.index = idx
				adm.release(provider, w.ID)
				results <- out
			}(i, task, w, agentID)
		}
		pending := 0
		for _, t := range s.run.Tasks {
			if t.Status == domain.TaskPending || t.Status == domain.TaskBlocked || t.Status == domain.TaskQueued || t.Status == domain.TaskRunning {
				pending++
			}
		}
		s.mu.Unlock()
		if pending == 0 && len(inFlight) == 0 {
			return nil
		}
		if pending == 0 && !dispatched && len(inFlight) == 0 {
			return nil
		}
		if dispatched && len(inFlight) > 0 {
			// Still wait for at least one result; otherwise the loop would spin.
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out := <-results:
			delete(inFlight, out.index)
			s.applyOutcome(ctx, repo, out)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Service) runTask(ctx context.Context, repo gitx.Repo, task domain.Task, w config.WorkerConfig, agentID string) (out struct {
	index int
	task  domain.Task
	usage domain.Usage
	err   error
}) {
	out.task = task
	client, model, err := s.client(w.Model)
	if err != nil {
		out.err = err
		out.task.Status = domain.TaskFailed
		return
	}
	s.mu.Lock()
	run := cloneRun(s.run)
	s.mu.Unlock()
	short := strings.ReplaceAll(run.ID, "run_", "")
	if len(short) > 12 {
		short = short[:12]
	}
	taskBranch := fmt.Sprintf("%s/tasks/%s-a%d", strings.TrimSuffix(run.IntegrationBranch, "/integration"), sanitize(task.ID), task.Attempts)
	taskDir := filepath.Join(run.RunDir, "tasks", sanitize(task.ID)+fmt.Sprintf("-a%d", task.Attempts))
	base, err := (gitx.Repo{Dir: run.IntegrationDir}).Head(ctx)
	if err != nil {
		out.err = err
		out.task.Status = domain.TaskFailed
		return
	}
	if err := repo.WorktreeAdd(ctx, taskDir, taskBranch, base); err != nil {
		out.err = err
		out.task.Status = domain.TaskFailed
		return
	}
	out.task.Branch = taskBranch
	out.task.Worktree = taskDir
	s.mu.Lock()
	for i := range s.run.Tasks {
		if s.run.Tasks[i].ID == task.ID {
			s.run.Tasks[i].Branch = taskBranch
			s.run.Tasks[i].Worktree = taskDir
		}
	}
	s.mu.Unlock()
	s.save()

	reg := tools.Builtins()
	if len(w.Tools) > 0 {
		reg = reg.Only(w.Tools...)
	}
	tools.AddWebTools(reg, s.cfg.Search)
	if s.mcp != nil {
		s.mcp.AddTo(reg)
	}
	if s.jobs != nil {
		s.jobs.AddTo(reg)
	}
	if s.tasks != nil {
		reg.Add(suggestTaskTool{tr: s.tasks})
		reg.Add(listTasksTool{tr: s.tasks})
		reg.Add(commentTaskTool{tr: s.tasks})
	}
	reg.Add(listWorkersTool{profile: s.profile})
	if s.questions != nil {
		reg.Add(questions.AskTool{Svc: s.questions, Autonomous: true})
	}
	system := prompt.WorkerSystem(s.cfg, task, w, taskDir)
	effort := w.Effort
	if effort == "" {
		effort = "medium"
	}
	rt := agent.New(agent.Config{
		AgentID: agentID, SessionID: s.sessionID, RunID: run.ID, TaskID: firstNonEmpty(task.BoardID, task.ID),
		Model: model, Client: client, System: system, Effort: effort, MaxSteps: w.MaxSteps,
		Cwd: taskDir, Mode: domain.ModeBuild, Tools: reg, Events: s.bus,
		Permissions: tools.Env{Cwd: taskDir, Mode: domain.ModeBuild, Permissions: s.cfg.Permissions},
	})
	res, err := rt.Run(ctx, []domain.Message{{
		ID: domain.NewID("msg"), Role: domain.RoleUser, CreatedAt: time.Now(),
		Content: "Execute the assigned task now. Inspect the worktree, implement the change, verify it, and leave the result committed.",
	}})
	out.usage = res.Usage
	out.task.Usage.Add(res.Usage)
	s.mu.Lock()
	for _, rec := range res.Records {
		s.run.Ledger.Add(rec)
		s.recordUsage(rec, rec.AgentID)
	}
	s.mu.Unlock()
	if err != nil {
		out.err = err
		out.task.Status = domain.TaskFailed
		out.task.Error = err.Error()
		out.task.Summary = lastAssistant(res.Messages)
		return
	}
	out.task.Summary = lastAssistant(res.Messages)
	commit, err := (gitx.Repo{Dir: taskDir}).CommitAll(ctx, "conclave: "+task.ID+" "+task.Title)
	if err != nil {
		out.err = err
		out.task.Status = domain.TaskFailed
		out.task.Error = err.Error()
		return
	}
	out.task.Commit = commit
	if diff, derr := (gitx.Repo{Dir: taskDir}).Diff(ctx, base); derr == nil {
		out.task.Diff = clipText(diff, 40000)
	}
	out.task.Status = domain.TaskDone
	return
}

func (s *Service) applyOutcome(ctx context.Context, repo gitx.Repo, out struct {
	index int
	task  domain.Task
	usage domain.Usage
	err   error
}) {
	s.mu.Lock()
	var runCopy *domain.Run
	if s.run != nil {
		runCopy = cloneRun(s.run)
	}
	if out.index >= 0 && out.index < len(s.run.Tasks) {
		t := &s.run.Tasks[out.index]
		t.Usage = out.task.Usage
		t.Commit = out.task.Commit
		t.Summary = out.task.Summary
		t.Diff = out.task.Diff
		t.Error = out.task.Error
		if out.err != nil {
			t.Status = domain.TaskFailed
			if t.Error == "" {
				t.Error = out.err.Error()
			}
			if s.tasks != nil && t.BoardID != "" {
				_, _ = s.tasks.Transition(ctx, t.BoardID, tracker.StatusBlocked, "conclave")
			}
		} else {
			t.Status = domain.TaskReview
			if s.tasks != nil && t.BoardID != "" {
				_, _ = s.tasks.Transition(ctx, t.BoardID, tracker.StatusReview, "conclave")
			}
		}
		now := time.Now()
		t.FinishedAt = &now
	}
	s.mu.Unlock()

	if out.err == nil && runCopy != nil && out.task.Commit != "" {
		integration := gitx.Repo{Dir: runCopy.IntegrationDir}
		if err := integration.Merge(ctx, out.task.Branch, "conclave: merge "+out.task.ID+" "+out.task.Title); err != nil {
			s.mu.Lock()
			if out.index < len(s.run.Tasks) {
				t := &s.run.Tasks[out.index]
				t.Status = domain.TaskFailed
				t.Error = "merge conflict: " + err.Error()
				if s.tasks != nil && t.BoardID != "" {
					_, _ = s.tasks.Transition(ctx, t.BoardID, tracker.StatusBlocked, "conclave")
				}
			}
			s.mu.Unlock()
			s.publish("merge.conflict", err.Error(), map[string]any{"task_id": out.task.ID})
		} else {
			s.mu.Lock()
			if out.index < len(s.run.Tasks) {
				s.run.Tasks[out.index].Status = domain.TaskDone
			}
			s.mu.Unlock()
			s.publish("merge.completed", "", map[string]any{"task_id": out.task.ID})
		}
	}
	s.mu.Lock()
	if s.run != nil {
		s.run.Usage.Add(out.usage)
	}
	s.mu.Unlock()
	s.save()
	s.publish("task.finished", "", map[string]any{"task": out.task, "error": errorString(out.err)})
}

func (s *Service) reviewWithMaster(ctx context.Context) error {
	s.mu.Lock()
	s.run.Phase = domain.RunReviewing
	runID := s.run.ID
	masterRef := s.MasterModel()
	s.mu.Unlock()
	report := s.buildReviewReport(ctx)
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunReviewing, "run_id": runID})

	client, model, err := s.client(masterRef)
	if err != nil {
		return err
	}
	// Review is intentionally tool-light: the master must make decisions from
	// the result versions and suggestions instead of inspecting every worktree.
	reg := tools.NewRegistry()
	reg.Add(submitPlanTool{svc: s})
	reg.Add(finishRunTool{svc: s})
	if s.tasks != nil {
		reg.Add(createTaskTool{tr: s.tasks})
		reg.Add(listTasksTool{tr: s.tasks})
		reg.Add(commentTaskTool{tr: s.tasks})
		reg.Add(reviewResultsTool{svc: s})
	}
	reg.Add(listWorkersTool{profile: s.profile})
	if s.questions != nil {
		reg.Add(questions.AskTool{Svc: s.questions})
	}
	effort := s.profile.Master.Effort
	if effort == "" {
		effort = "high"
	}
	streamed := false
	rt := agent.New(agent.Config{
		AgentID: "master-review", SessionID: s.sessionID, RunID: runID,
		Model: model, Client: client, System: prompt.ReviewSystem(s.cfg, s.run),
		Effort: effort, MaxSteps: s.profile.Master.MaxSteps, Cwd: s.cwd, Mode: domain.ModePlan, Tools: reg, Events: s.bus,
		Permissions: tools.Env{Cwd: s.cwd, Mode: domain.ModePlan, Permissions: s.cfg.Permissions},
		OnMessage: func(msg domain.Message) {
			streamed = true
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.persistMessagesLocked()
			s.mu.Unlock()
		},
	})
	res, err := rt.Run(ctx, []domain.Message{{
		ID: domain.NewID("msg"), Role: domain.RoleUser, CreatedAt: time.Now(),
		Content: report + "\n\nReview every task whose status is review. Apply all decisions in ONE review_results call " +
			"(approve or reject each task, with concrete feedback for rejections). Do not call approve_result/reject_result " +
			"once per task. Workers may only leave suggestions; if a follow-up is needed, create it yourself with create_task " +
			"after reviewing. If work remains, call submit_plan with the next isolated task pool. If the goal and acceptance " +
			"criteria are met, call finish_run with a concise final summary.",
	}})
	s.mu.Lock()
	if !streamed {
		s.messages = append(s.messages, res.Messages...)
	}
	s.run.Usage.Add(res.Usage)
	for _, rec := range res.Records {
		s.run.Ledger.Add(rec)
		s.recordUsage(rec, rec.AgentID)
	}
	s.mu.Unlock()
	s.persistMessages()
	s.save()
	return err
}

func (s *Service) buildReviewReport(ctx context.Context) string {
	s.mu.Lock()
	run := cloneRun(s.run)
	s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", run.Goal)
	if run.Plan != nil && len(run.Plan.Acceptance) > 0 {
		fmt.Fprintf(&b, "Acceptance: %s\n", strings.Join(run.Plan.Acceptance, "; "))
	}
	b.WriteString("\nTasks:\n")
	for _, t := range run.Tasks {
		fmt.Fprintf(&b, "- [%s] board=%s %s (%s) commit=%s error=%s\n", t.Status, t.BoardID, t.ID+": "+t.Title, t.WorkerID, short(t.Commit, 8), t.Error)
		if t.Summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", clipText(t.Summary, 1200))
		}
		if t.Branch != "" {
			fmt.Fprintf(&b, "  branch: %s\n", t.Branch)
		}
		if s.tasks == nil || t.BoardID == "" {
			continue
		}
		if results, err := s.tasks.Results(ctx, t.BoardID); err == nil && len(results) > 0 {
			latest := results[0]
			fmt.Fprintf(&b, "  result v%d (%s): %s\n", len(results), latest.Verdict, clipText(latest.Content, 2500))
		}
		if comments, err := s.tasks.Comments(ctx, t.BoardID); err == nil {
			for _, c := range comments {
				if strings.HasPrefix(c.Body, "SUGGESTION") {
					fmt.Fprintf(&b, "  worker suggestion: %s\n", clipText(c.Body, 1200))
				}
			}
		}
	}
	return b.String()
}

func (s *Service) approveBoardTask(ctx context.Context, boardID, actor, feedback string) error {
	if s.tasks == nil {
		return fmt.Errorf("task tracker unavailable")
	}
	if strings.TrimSpace(feedback) != "" {
		_, _ = s.tasks.AddComment(ctx, boardID, actor, feedback)
	}
	if _, err := s.tasks.Transition(ctx, boardID, tracker.StatusDone, actor); err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.run.Tasks {
		if s.run.Tasks[i].BoardID == boardID {
			s.run.Tasks[i].Status = domain.TaskDone
		}
	}
	s.mu.Unlock()
	s.save()
	return nil
}

func (s *Service) rejectBoardTask(ctx context.Context, boardID, actor, feedback string) error {
	if s.tasks == nil {
		return fmt.Errorf("task tracker unavailable")
	}
	if strings.TrimSpace(feedback) != "" {
		_, _ = s.tasks.AddComment(ctx, boardID, actor, feedback)
	}
	if _, err := s.tasks.Transition(ctx, boardID, tracker.StatusReady, actor); err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.run.Tasks {
		if s.run.Tasks[i].BoardID == boardID {
			s.run.Tasks[i].Status = domain.TaskPending
		}
	}
	s.newPool = true
	s.mu.Unlock()
	s.save()
	return nil
}

func buildReport(run *domain.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", run.Goal)
	if run.Plan != nil && len(run.Plan.Acceptance) > 0 {
		fmt.Fprintf(&b, "Acceptance: %s\n", strings.Join(run.Plan.Acceptance, "; "))
	}
	b.WriteString("\nTasks:\n")
	for _, t := range run.Tasks {
		fmt.Fprintf(&b, "- [%s] %s (%s) commit=%s error=%s\n", t.Status, t.ID+": "+t.Title, t.WorkerID, short(t.Commit, 8), t.Error)
		if t.Summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", clipText(t.Summary, 1200))
		}
		if t.Branch != "" {
			fmt.Fprintf(&b, "  branch: %s\n", t.Branch)
		}
	}
	return b.String()
}

func (s *Service) fail(msg string) {
	s.mu.Lock()
	if s.run != nil {
		s.run.Phase = domain.RunFailed
		s.run.Error = msg
	}
	s.running = false
	s.mu.Unlock()
	s.save()
	s.publish("run.phase_changed", "", map[string]any{"phase": domain.RunFailed, "error": msg})
}

func (s *Service) workerStrict(id string) (config.WorkerConfig, error) {
	for _, w := range s.profile.Workers {
		if w.ID == id {
			return w, nil
		}
	}
	return config.WorkerConfig{}, fmt.Errorf("worker profile %q not found", id)
}

func (s *Service) defaultWorker(kind string) config.WorkerConfig {
	if kind == "research" {
		for _, w := range s.profile.Workers {
			if w.ID == "researcher" {
				return w
			}
		}
	}
	for _, w := range s.profile.Workers {
		if w.ID == "coder" {
			return w
		}
	}
	for _, w := range s.profile.Workers {
		return w
	}
	return config.WorkerConfig{}
}

func (s *Service) worker(id string) (config.WorkerConfig, error) {
	for _, w := range s.profile.Workers {
		if w.ID == id {
			return w, nil
		}
	}
	if id == "" && len(s.profile.Workers) > 0 {
		return s.profile.Workers[0], nil
	}
	return config.WorkerConfig{}, fmt.Errorf("worker profile %q not found", id)
}

func (s *Service) runID() string {
	v, _ := s.runIDValue.Load().(string)
	return v
}

func (s *Service) runPhase() domain.RunPhase {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return ""
	}
	return s.run.Phase
}

func (s *Service) save() {
	s.mu.Lock()
	run := cloneRun(s.run)
	s.mu.Unlock()
	if run == nil || s.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.store.SaveRun(ctx, run)
}

func (s *Service) publish(typ, text string, payload any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(domain.Event{Time: time.Now(), SessionID: s.sessionID, RunID: s.runID(), Type: typ, Text: text, Payload: payload})
}

func cloneRun(r *domain.Run) *domain.Run {
	if r == nil {
		return nil
	}
	b, _ := json.Marshal(r)
	var out domain.Run
	_ = json.Unmarshal(b, &out)
	return &out
}

func normalizeTasks(tasks []domain.Task) {
	for i := range tasks {
		if tasks[i].Status == "" {
			tasks[i].Status = domain.TaskPending
		}
	}
}

func findTask(tasks []domain.Task, id string) *domain.Task {
	for i := range tasks {
		if tasks[i].ID == id {
			return &tasks[i]
		}
	}
	return nil
}

func lastAssistant(msgs []domain.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == domain.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func maxInt(v, min int) int {
	if v < min {
		return min
	}
	if v > 5 {
		return 5
	}
	return v
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
