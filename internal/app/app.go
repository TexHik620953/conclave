package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/agent"
	"github.com/texhik/conclave/internal/board"
	"github.com/texhik/conclave/internal/conclave"
	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
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

type App struct {
	mu        sync.Mutex
	cfg       *config.Config
	profile   config.Profile
	store     *store.Store
	bus       *event.Bus
	session   *domain.Session
	cwd       string
	clients   map[string]llm.Client
	clientMu  sync.Mutex
	busy      bool
	concl     *conclave.Service
	conclKey  string
	mcpMgr    *mcp.Manager
	jobMgr    *jobs.Manager
	track     *tracker.Service
	usage     *usage.Service
	questions *questions.Service
	queue     *board.Runner
}

type SessionInfo struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Mode      domain.Mode `json:"mode"`
	Model     string      `json:"model"`
	UpdatedAt time.Time   `json:"updated_at"`
	Active    bool        `json:"active"`
}

type State struct {
	Session      *domain.Session `json:"session"`
	Cwd          string          `json:"cwd"`
	ProfileName  string          `json:"profile_name"`
	ContextLimit int             `json:"context_limit"`
	Model        llm.Model       `json:"model"`
	Busy         bool            `json:"busy"`
}

func New(ctx context.Context, cwd, profileName string) (*App, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(abs)
	if err != nil {
		return nil, err
	}
	if profileName == "" {
		profileName = cfg.DefaultProfile
	}
	profile, err := cfg.Profile(profileName)
	if err != nil {
		return nil, err
	}
	if profile.Agent.Model == "" {
		return nil, fmt.Errorf("profile %q has no agent.model", profileName)
	}
	if err := os.MkdirAll(filepath.Join(abs, ".conclave"), 0o755); err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(abs, ".conclave", "conclave.db"))
	if err != nil {
		return nil, err
	}
	tr, err := tracker.New(st.DB(), abs)
	if err != nil {
		st.Close()
		return nil, err
	}
	usg, err := usage.New(st.DB())
	if err != nil {
		st.Close()
		return nil, err
	}
	bus := event.New()
	qsvc, err := questions.New(st.DB(), tr, bus)
	if err != nil {
		st.Close()
		return nil, err
	}
	a := &App{
		cfg: cfg, profile: profile, store: st, bus: bus,
		cwd: abs, clients: map[string]llm.Client{},
		mcpMgr:    mcp.Connect(ctx, cfg.MCP),
		jobMgr:    jobs.New(bus, ""),
		track:     tr,
		usage:     usg,
		questions: qsvc,
	}
	if n, err := st.ImportLegacy(ctx, abs); err == nil && n > 0 {
		bus.Publish(domain.Event{Time: time.Now(), Type: "session.imported", Text: fmt.Sprintf("imported %d legacy session(s)", n)})
	}
	sess, err := st.LatestSession(ctx, abs)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.queue = board.New(board.Deps{Config: cfg, Profile: profile, Tracker: tr, Bus: bus, Cwd: abs, Client: a.clientUnlocked, Usage: usg, Questions: qsvc})
	if sess == nil {
		sess = &domain.Session{
			ID: domain.NewID("ses"), Title: "New session", Cwd: abs,
			Mode: domain.ModeBuild, Model: profile.Agent.Model, Effort: profile.Agent.Effort,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
	}
	if sess.Mode == domain.ModePlan {
		sess.Mode = domain.ModeBuild
		sess.PlanOnly = true
	}
	if sess.Model == "" {
		sess.Model = profile.Agent.Model
	}
	if sess.Effort == "" {
		sess.Effort = profile.Agent.Effort
	}
	a.session = sess
	if _, _, err := a.clientUnlocked(sess.Model); err != nil {
		sess.Model = profile.Agent.Model
		if _, _, err := a.clientUnlocked(sess.Model); err != nil {
			st.Close()
			return nil, err
		}
	}
	repairSession(sess)
	a.ensureConclave()
	recovered, _ := tr.RecoverInterrupted(ctx, "server restarted while the task was running")
	if recovered > 0 {
		bus.Publish(domain.Event{Time: time.Now(), Type: "board.changed", Text: fmt.Sprintf("recovered %d interrupted task(s)", recovered)})
	}
	if recovered > 0 || tr.QueueRunning(ctx) {
		_ = a.queue.Start(context.Background())
	}
	if err := st.SaveSession(ctx, sess); err != nil {
		st.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) Close() error {
	if a.mcpMgr != nil {
		a.mcpMgr.Close()
	}
	if a.jobMgr != nil {
		a.jobMgr.Close()
	}
	if a.bus != nil {
		a.bus.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

func (a *App) Bus() *event.Bus               { return a.bus }
func (a *App) Cwd() string                   { return a.cwd }
func (a *App) Tracker() *tracker.Service     { return a.track }
func (a *App) Usage() *usage.Service         { return a.usage }
func (a *App) Questions() *questions.Service { return a.questions }

func (a *App) ProfileConfig() config.Profile {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.profile
}
func (a *App) ProfileName() string {
	return a.cfg.DefaultProfile
}

func (a *App) Sessions(ctx context.Context) ([]SessionInfo, error) {
	list, err := a.store.ListSessions(ctx, 100)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	active := ""
	if a.session != nil {
		active = a.session.ID
	}
	a.mu.Unlock()
	out := make([]SessionInfo, 0, len(list))
	for _, s := range list {
		out = append(out, SessionInfo{ID: s.ID, Title: s.Title, Mode: s.Mode, Model: s.Model, UpdatedAt: s.UpdatedAt, Active: s.ID == active})
	}
	return out, nil
}

func (a *App) ActiveSessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return ""
	}
	return a.session.ID
}

func (a *App) OpenSession(ctx context.Context, id string) error {
	a.mu.Lock()
	if a.busy || (a.concl != nil && a.concl.Busy()) {
		a.mu.Unlock()
		return fmt.Errorf("cannot switch session while an agent or Conclave run is active")
	}
	if a.session != nil && a.session.ID == id {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	sess, err := a.store.LoadSession(ctx, id)
	if err != nil {
		return err
	}
	if sess.Mode == domain.ModePlan {
		sess.Mode = domain.ModeBuild
		sess.PlanOnly = true
	}
	repairSession(sess)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.cfg.ResolveModel(sess.Model); err != nil {
		sess.Model = a.profile.Agent.Model
	}
	if a.session != nil {
		_ = a.store.SaveSession(ctx, a.session)
	}
	a.session = sess
	a.conclKey = ""
	a.ensureConclave()
	return a.store.SaveSession(ctx, sess)
}

func (a *App) DeleteSession(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy || (a.concl != nil && a.concl.Busy()) {
		return fmt.Errorf("cannot delete a session while an agent or Conclave run is active")
	}
	wasActive := a.session != nil && a.session.ID == id
	if err := a.store.DeleteSession(ctx, id); err != nil {
		return err
	}
	if !wasActive {
		return nil
	}
	a.session = &domain.Session{
		ID: domain.NewID("ses"), Title: "New session", Cwd: a.cwd,
		Mode: domain.ModeBuild, Model: a.profile.Agent.Model, Effort: a.profile.Agent.Effort,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	a.conclKey = ""
	a.ensureConclave()
	return a.store.SaveSession(ctx, a.session)
}

func repairMessages(msgs []domain.Message) []domain.Message {
	existing := map[string]bool{}
	for _, m := range msgs {
		if m.Role == domain.RoleTool && m.ToolCallID != "" {
			existing[m.ToolCallID] = true
		}
	}
	var missing []domain.Message
	for _, m := range msgs {
		if m.Role != domain.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && !existing[tc.ID] {
				existing[tc.ID] = true
				missing = append(missing, domain.Message{
					ID: domain.NewID("tool"), Role: domain.RoleTool, ToolCallID: tc.ID, Name: tc.Name,
					Content:   "ERROR: tool execution was interrupted by a server restart. Continue from here; retry the tool if needed.",
					CreatedAt: time.Now(),
				})
			}
		}
	}
	return append(msgs, missing...)
}

func repairSession(sess *domain.Session) {
	if sess == nil {
		return
	}
	sess.Messages = repairMessages(sess.Messages)
	sess.ConclaveMessages = repairMessages(sess.ConclaveMessages)
}

func (a *App) State() State {
	a.mu.Lock()
	sess := cloneSession(a.session)
	cfg := a.cfg
	cwd := a.cwd
	busy := a.busy
	concl := a.concl
	a.mu.Unlock()

	// Never call into Conclave while holding App.mu: Conclave callbacks can
	// acquire App.mu (for example Pause -> PauseQueue -> App.Queue).
	if concl != nil && concl.Busy() {
		busy = true
	}
	model, err := cfg.ResolveModel(sess.Model)
	info := llm.Model{}
	limit := 0
	if err == nil {
		info = llm.ModelFromRef(sess.Model, model)
		limit = info.Context
	}
	return State{Session: sess, Cwd: cwd, ProfileName: cfg.DefaultProfile, ContextLimit: limit, Model: info, Busy: busy}
}

func (a *App) Conclave() *conclave.Service {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureConclave()
	return a.concl
}

func (a *App) ensureConclave() {
	key := a.session.ID + "|" + a.cwd
	if a.concl != nil && a.conclKey == key {
		return
	}
	a.conclKey = key
	a.concl = conclave.New(conclave.Deps{
		Config: a.cfg, Profile: a.profile, Store: a.store, Bus: a.bus,
		SessionID: a.session.ID, Cwd: a.cwd, Client: a.clientUnlocked, MCP: a.mcpMgr, Jobs: a.jobMgr, Tasks: a.track, Usage: a.usage, Questions: a.questions, StartQueue: func(ctx context.Context) error { return a.Queue().Start(ctx) }, PauseQueue: func() { a.PauseQueue() },
	})
}

func (a *App) clientUnlocked(ref string) (llm.Client, llm.Model, error) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	m, err := cfg.ResolveModel(ref)
	if err != nil {
		return nil, llm.Model{}, err
	}
	info := llm.ModelFromRef(ref, m)
	if c, ok := a.clients[m.Provider]; ok {
		return c, info, nil
	}
	c := llm.NewOpenAI(m.ProviderConfig)
	a.clients[m.Provider] = c
	return c, info, nil
}

func (a *App) Send(ctx context.Context, text string) error {
	return a.send(ctx, text, true)
}

func (a *App) send(ctx context.Context, text string, appendUser bool) error {
	text = strings.TrimSpace(text)
	if appendUser && text == "" {
		return nil
	}
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return fmt.Errorf("agent is busy")
	}
	if a.session.Mode == domain.ModeConclave {
		a.mu.Unlock()
		if appendUser {
			return a.Conclave().Send(ctx, text)
		}
		return fmt.Errorf("conclave history regeneration is not available without an index")
	}
	a.busy = true
	sess := a.session
	if appendUser {
		sess.Messages = append(sess.Messages, domain.Message{
			ID: domain.NewID("msg"), Role: domain.RoleUser, Content: text, CreatedAt: time.Now(),
		})
		if sess.Title == "" || sess.Title == "New session" {
			sess.Title = shortTitle(text)
		}
	}
	history := append([]domain.Message(nil), sess.Messages...)
	mode := sess.Mode
	planOnly := sess.PlanOnly
	modelRef := sess.Model
	effort := sess.Effort
	a.mu.Unlock()
	if appendUser {
		_ = a.store.SaveSession(context.Background(), sess)
	}
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	client, model, err := a.clientUnlocked(modelRef)
	if err != nil {
		return err
	}
	var reg *tools.Registry
	agentMode := mode
	extraPrompt := ""
	if planOnly {
		agentMode = domain.ModePlan
		reg = tools.ReadOnly()
		extraPrompt = "Plan-only mode is enabled. Do not modify files or run mutating commands. Produce a concrete plan and create board tasks for follow-up work if useful."
	} else if mode == domain.ModePlan {
		agentMode = domain.ModePlan
		reg = tools.ReadOnly()
	} else {
		reg = tools.Builtins()
		if a.mcpMgr != nil {
			a.mcpMgr.AddTo(reg)
		}
		if a.jobMgr != nil {
			a.jobMgr.AddTo(reg)
		}
	}
	if a.track != nil {
		for _, t := range conclave.TaskTools(a.track) {
			reg.Add(t)
		}
	}
	if a.questions != nil {
		reg.Add(questions.AskTool{Svc: a.questions})
	}
	system := prompt.Build(a.cfg, a.profile, agentMode, a.cwd, extraPrompt)
	rt := agent.New(agent.Config{
		AgentID: "main", SessionID: sess.ID, Model: model, Client: client,
		System: system, Effort: effort, Cwd: a.cwd, Mode: mode, Tools: reg,
		Events:      a.bus,
		Permissions: tools.Env{Cwd: a.cwd, Mode: mode, Permissions: a.cfg.Permissions},
	})
	res, err := rt.Run(ctx, history)
	a.mu.Lock()
	sess.Messages = append(sess.Messages, res.Messages...)
	sess.Usage.Add(res.Usage)
	for _, rec := range res.Records {
		sess.Ledger.Add(rec)
		if a.usage != nil {
			_ = a.usage.Add(context.Background(), usage.Record{
				SessionID: sess.ID, AgentID: rec.AgentID, Provider: rec.Provider, Model: rec.Model,
				Source: "chat", InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens,
				CachedTokens: rec.CachedTokens, ReasoningTokens: rec.ReasoningTokens,
				Requests: rec.Requests, LastContext: rec.LastContext, ContextWindow: rec.ContextWindow,
			})
		}
	}
	if res.LastPromptTokens > 0 {
		sess.ContextTokens = res.LastPromptTokens
	}
	sess.UpdatedAt = time.Now()
	a.mu.Unlock()
	if appendUser {
		_ = a.store.SaveSession(context.Background(), sess)
	}
	if err != nil {
		a.bus.Publish(domain.Event{SessionID: sess.ID, Type: "session.error", Text: err.Error(), Time: time.Now()})
		return err
	}
	return nil
}

func (a *App) SetMode(mode domain.Mode) error {
	if mode == domain.ModePlan {
		mode = domain.ModeBuild
	}
	if mode != domain.ModeBuild && mode != domain.ModeConclave {
		return fmt.Errorf("unknown mode %q", mode)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return fmt.Errorf("cannot switch mode while the agent is running")
	}
	if a.concl != nil && a.concl.Running() {
		return fmt.Errorf("cannot switch mode while a Conclave run is active")
	}
	a.session.Mode = mode
	if mode == domain.ModeBuild {
		// plan_only remains a separate permission toggle.
	}
	if mode == domain.ModeConclave {
		a.ensureConclave()
	}
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) SetPlanOnly(v bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return fmt.Errorf("cannot change plan mode while the agent is running")
	}
	if a.session.Mode == domain.ModeConclave {
		return fmt.Errorf("Conclave master is already read-only")
	}
	a.session.PlanOnly = v
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) SetModel(ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.cfg.ResolveModel(ref); err != nil {
		return err
	}
	a.session.Model = ref
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) SetEffort(effort string) error {
	switch effort {
	case "low", "medium", "high", "ultra":
	default:
		return fmt.Errorf("effort must be low, medium, high or ultra")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session.Effort = effort
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) Chdir(path string) error {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", abs)
	}
	cfg, err := config.Load(abs)
	if err != nil {
		return err
	}
	profile, err := cfg.Profile(cfg.DefaultProfile)
	if err != nil {
		return err
	}
	newMCP := mcp.Connect(context.Background(), cfg.MCP)
	newTracker, err := tracker.New(a.store.DB(), abs)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpMgr != nil {
		a.mcpMgr.Close()
	}
	a.mcpMgr = newMCP
	a.cfg = cfg
	a.profile = profile
	a.cwd = abs
	a.track = newTracker
	a.conclKey = ""
	a.session.Cwd = abs
	a.clients = map[string]llm.Client{}
	if _, err := a.cfg.ResolveModel(a.session.Model); err != nil {
		a.session.Model = profile.Agent.Model
	}
	a.ensureConclave()
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) NewSession() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return fmt.Errorf("cannot start a new session while the agent is running")
	}
	a.session = &domain.Session{
		ID: domain.NewID("ses"), Title: "New session", Cwd: a.cwd,
		Mode: domain.ModeBuild, Model: a.profile.Agent.Model, Effort: a.profile.Agent.Effort,
		PlanOnly: false, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	a.conclKey = ""
	a.ensureConclave()
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) Revert(ctx context.Context, keep int) error {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return fmt.Errorf("cannot revert while the agent is running")
	}
	if a.session.Mode == domain.ModeConclave {
		concl := a.concl
		a.mu.Unlock()
		if concl == nil || concl.Busy() {
			return fmt.Errorf("cannot revert while Conclave is running")
		}
		concl.Revert(keep)
		return nil
	}
	defer a.mu.Unlock()
	if keep < 0 {
		keep = 0
	}
	if keep > len(a.session.Messages) {
		keep = len(a.session.Messages)
	}
	a.session.Messages = append([]domain.Message(nil), a.session.Messages[:keep]...)
	a.session.UpdatedAt = time.Now()
	a.conclKey = ""
	a.ensureConclave()
	return a.store.SaveSession(ctx, a.session)
}

func (a *App) Fork(ctx context.Context, keep int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy || (a.concl != nil && a.concl.Busy()) {
		return fmt.Errorf("cannot fork while an agent is running")
	}
	if keep < 0 {
		keep = 0
	}
	now := time.Now()
	fork := &domain.Session{
		ID: domain.NewID("ses"), Title: "Fork · " + a.session.Title, Cwd: a.session.Cwd,
		Mode: a.session.Mode, Model: a.session.Model, Effort: a.session.Effort, PlanOnly: a.session.PlanOnly,
		CreatedAt: now, UpdatedAt: now,
	}
	if a.session.Mode == domain.ModeConclave {
		if keep > len(a.session.ConclaveMessages) {
			keep = len(a.session.ConclaveMessages)
		}
		fork.ConclaveMessages = append([]domain.Message(nil), a.session.ConclaveMessages[:keep]...)
	} else {
		if keep > len(a.session.Messages) {
			keep = len(a.session.Messages)
		}
		fork.Messages = append([]domain.Message(nil), a.session.Messages[:keep]...)
	}
	_ = a.store.SaveSession(ctx, a.session)
	a.session = fork
	a.conclKey = ""
	a.ensureConclave()
	return a.store.SaveSession(ctx, fork)
}

func (a *App) Continue(ctx context.Context) error {
	return a.Send(ctx, "Continue from where you left off. Do not restart or repeat completed work. Inspect current progress and proceed with the remaining work.")
}

func (a *App) Regenerate(ctx context.Context) error {
	return a.RegenerateAt(ctx, -1)
}

func (a *App) RegenerateAt(ctx context.Context, index int) error {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return fmt.Errorf("agent is busy")
	}
	if a.session.Mode == domain.ModeConclave {
		a.mu.Unlock()
		return a.Conclave().RegenerateAt(ctx, index)
	}
	msgs := a.session.Messages
	if index < 0 {
		// Retry the current turn. If the assistant response was lost in a
		// crash, keep the user message and run the model again from there.
		lastUser := -1
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == domain.RoleUser {
				lastUser = i
				break
			}
		}
		if lastUser >= 0 {
			for i := len(msgs) - 1; i > lastUser; i-- {
				if msgs[i].Role == domain.RoleAssistant {
					index = i
					break
				}
			}
			if index < 0 {
				index = len(msgs)
			}
		} else {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == domain.RoleAssistant {
					index = i
					break
				}
			}
		}
	}
	if index > len(msgs) || (index < len(msgs) && msgs[index].Role != domain.RoleAssistant) {
		a.mu.Unlock()
		return fmt.Errorf("no assistant message to regenerate")
	}
	a.session.Messages = append([]domain.Message(nil), msgs[:index]...)
	a.session.UpdatedAt = time.Now()
	_ = a.store.SaveSession(ctx, a.session)
	a.mu.Unlock()
	return a.send(ctx, "", false)
}

func (a *App) Compact(ctx context.Context) error {
	a.mu.Lock()
	if a.busy || len(a.session.Messages) < 4 {
		a.mu.Unlock()
		return fmt.Errorf("not enough conversation to compact, or agent is busy")
	}
	mode := a.session.Mode
	modelRef := a.session.Model
	history := append([]domain.Message(nil), a.session.Messages...)
	sess := a.session
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()
	client, model, err := a.clientUnlocked(modelRef)
	if err != nil {
		return err
	}
	system := "Summarize the conversation for a coding agent. Preserve decisions, file paths, constraints, unfinished work and errors. Be compact but complete."
	msgs := []domain.Message{{Role: domain.RoleSystem, Content: system}, {Role: domain.RoleUser, Content: transcript(history)}}
	res, err := llm.Collect(ctx, client, model, llm.Request{Messages: msgs, Effort: "low"})
	if err != nil {
		return err
	}
	summary := domain.Message{
		ID: domain.NewID("msg"), Role: domain.RoleAssistant, CreatedAt: time.Now(),
		Content: "Conversation summary:\n\n" + res.Content,
	}
	a.mu.Lock()
	sess.Messages = []domain.Message{summary}
	sess.Usage.Add(res.Usage)
	sess.Ledger.Add(domain.UsageRecord{
		Provider: model.Provider, Model: model.ID, AgentID: "compact",
		InputTokens: res.Usage.PromptTokens, OutputTokens: res.Usage.CompletionTokens,
		CachedTokens: res.Usage.CachedTokens, ReasoningTokens: res.Usage.ReasoningTokens,
		Requests: res.Usage.Requests, LastContext: res.Usage.PromptTokens, ContextWindow: model.Context,
	})
	if a.usage != nil {
		_ = a.usage.Add(context.Background(), usage.Record{
			SessionID: sess.ID, AgentID: "compact", Provider: model.Provider, Model: model.ID, Source: "compact",
			InputTokens: res.Usage.PromptTokens, OutputTokens: res.Usage.CompletionTokens,
			CachedTokens: res.Usage.CachedTokens, ReasoningTokens: res.Usage.ReasoningTokens,
			Requests: res.Usage.Requests, LastContext: res.Usage.PromptTokens, ContextWindow: model.Context,
		})
	}
	sess.ContextTokens = res.Usage.PromptTokens
	if mode == domain.ModeConclave {
		// The Conclave master conversation is owned by its service.
	}
	a.mu.Unlock()
	return a.store.SaveSession(context.Background(), sess)
}

func (a *App) Queue() *board.Runner {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queue == nil {
		a.queue = board.New(board.Deps{
			Config: a.cfg, Profile: a.profile, Tracker: a.track, Bus: a.bus,
			Cwd: a.cwd, Client: a.clientUnlocked, Usage: a.usage, Questions: a.questions,
		})
	}
	return a.queue
}

func (a *App) StartQueue(ctx context.Context) error {
	return a.Queue().Start(ctx)
}

func (a *App) PauseQueue() {
	a.Queue().Pause()
}

func (a *App) ReloadConfig() error {
	a.mu.Lock()
	busy := a.busy || (a.concl != nil && a.concl.Busy()) || (a.queue != nil && a.queue.Running())
	a.mu.Unlock()
	if busy {
		return fmt.Errorf("cannot reload config while an agent, Conclave run or queue is active")
	}
	return a.reloadConfig()
}

func (a *App) Providers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.cfg.Providers))
	for name := range a.cfg.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (a *App) ProviderModels(provider string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.cfg.Providers[provider]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(p.Models))
	for id := range p.Models {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (a *App) FetchModels(ctx context.Context, provider string) ([]llm.RemoteModel, error) {
	a.mu.Lock()
	p, ok := a.cfg.Providers[provider]
	if !ok {
		a.mu.Unlock()
		return nil, fmt.Errorf("provider %q not found", provider)
	}
	a.mu.Unlock()
	return llm.ListModels(ctx, p)
}

func (a *App) AddProvider(name, baseURL, apiKey string) error {
	name = strings.TrimSpace(strings.ToLower(name))
	baseURL = strings.TrimSpace(baseURL)
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if baseURL == "" {
		return fmt.Errorf("base URL is required")
	}
	a.mu.Lock()
	if a.busy || (a.concl != nil && a.concl.Busy()) || (a.queue != nil && a.queue.Running()) {
		a.mu.Unlock()
		return fmt.Errorf("cannot change providers while an agent or Conclave run is active")
	}
	a.mu.Unlock()
	if err := config.UpsertGlobalProvider(name, config.Provider{
		BaseURL: baseURL, APIKey: strings.TrimSpace(apiKey), MaxParallel: 8,
	}); err != nil {
		return err
	}
	return a.reloadConfig()
}

func (a *App) EnsureModel(provider, id string, contextWindow, maxOutput int) error {
	a.mu.Lock()
	if a.busy || (a.concl != nil && a.concl.Busy()) || (a.queue != nil && a.queue.Running()) {
		a.mu.Unlock()
		return fmt.Errorf("cannot add a model while an agent or Conclave run is active")
	}
	if _, err := a.cfg.ResolveModel(provider + "/" + id); err == nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	if maxOutput <= 0 {
		maxOutput = 8192
	}
	m := config.Model{
		Context: contextWindow, MaxOutput: maxOutput,
		Capabilities: config.Capabilities{Tools: true},
	}
	if err := config.UpsertGlobalModel(provider, id, m); err != nil {
		return err
	}
	return a.reloadConfig()
}

func (a *App) reloadConfig() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg, err := config.Load(a.cwd)
	if err != nil {
		return err
	}
	profile, err := cfg.Profile(cfg.DefaultProfile)
	if err != nil {
		return err
	}
	a.cfg = cfg
	a.profile = profile
	a.clients = map[string]llm.Client{}
	a.conclKey = ""
	a.queue = nil
	if _, err := a.cfg.ResolveModel(a.session.Model); err != nil {
		a.session.Model = profile.Agent.Model
	}
	if a.session.Mode == domain.ModeConclave {
		a.ensureConclave()
	}
	return a.store.SaveSession(context.Background(), a.session)
}

func (a *App) Models() []string {
	var out []string
	for pname, p := range a.cfg.Providers {
		for m := range p.Models {
			out = append(out, pname+"/"+m)
		}
	}
	return out
}

func (a *App) ModelInfo() llm.Model {
	st := a.State()
	return st.Model
}

func transcript(msgs []domain.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "  tool %s(%s)\n", tc.Name, tc.Arguments)
		}
	}
	return b.String()
}

func shortTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func cloneSession(s *domain.Session) *domain.Session {
	if s == nil {
		return nil
	}
	b, _ := json.Marshal(s)
	var out domain.Session
	_ = json.Unmarshal(b, &out)
	return &out
}
