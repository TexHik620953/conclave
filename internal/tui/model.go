package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/texhik/conclave/internal/app"
	"github.com/texhik/conclave/internal/conclave"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tracker"
)

type chatEntry struct {
	kind          string // user, assistant, tool, system, error
	agentID       string
	title         string
	content       string
	streaming     bool
	failed        bool
	duration      time.Duration
	created       time.Time
	rendered      string
	renderedWidth int
}

type agentInfo struct {
	ID      string
	Model   string
	TaskID  string
	Status  string
	Started time.Time
	Last    string
	Tools   []string
	Events  []string
}

type modalKind int

const (
	modalInfo modalKind = iota
	modalModels
	modalThemes
	modalUsage
	modalProviders
	modalAddProvider
	modalLoading
	modalTaskForm
	modalTaskDetail
	modalSessions
)

type modalItem struct {
	Label       string
	Value       string
	Description string
	Provider    string
	ModelID     string
	Context     int
	MaxOutput   int
}

type providerForm struct {
	Name  string
	URL   string
	Key   string
	Focus int
}

type taskForm struct {
	TaskID      string // non-empty when editing
	Title       string
	Description string
	Context     string
	Complexity  string
	Priority    string
	Component   string
	Labels      string
	Epic        string
	Focus       int
}

type modalState struct {
	kind     modalKind
	title    string
	body     string
	items    []modalItem
	sel      int
	usageTab int
	provider string
	form     providerForm
	taskForm taskForm
	taskID   string
}

type eventMsg domain.Event
type sendDoneMsg struct{ err error }
type actionMsg struct {
	err  error
	text string
}

type modelsLoadedMsg struct {
	provider string
	models   []llm.RemoteModel
	err      error
}

type paletteItem struct {
	Label       string
	Value       string
	Description string
	Spec        commandSpec
	IsArg       bool
}

type Model struct {
	app    *app.App
	events <-chan domain.Event
	unsub  func()

	state      app.State
	width      int
	height     int
	ready      bool
	mode       domain.Mode
	model      string
	effort     string
	cwd        string
	theme      Theme
	themeName  string
	paletteSel int

	input          textarea.Model
	chatVP         viewport.Model
	dashVP         viewport.Model
	spin           spinner.Model
	usage          domain.UsageLedger
	usageKey       string
	usageSel       int
	usageTab       int
	showSidebar    bool
	showToolDetail bool

	chat      []chatEntry
	streamIdx map[string]int
	toolIdx   map[string]int
	agents    map[string]*agentInfo

	tab      int // 0 chat, 1 tasks, 2 agents
	selTask  int
	selAgent int
	detail   bool
	run      *conclave.Snapshot

	modal         *modalState
	paletteForced bool
	history       []string
	histIdx       int

	busy        bool
	cancel      context.CancelFunc
	status      string
	errText     string
	toastText   string
	toastAt     time.Time
	phase       string
	planOnly    bool
	screen      int // 0 chat, 1 board
	boardTasks  []tracker.Task
	boardSel    int
	boardCol    int
	boardDetail bool
	boardSearch string
}

func New(a *app.App) *Model {
	lipgloss.SetHasDarkBackground(true)
	ta := textarea.New()
	ta.Placeholder = "Ask Conclave anything…  / commands   @ files"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Focus()
	ta.SetHeight(1)
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	events, unsub := a.Bus().Subscribe()
	st := a.State()
	m := &Model{
		app: a, events: events, unsub: unsub,
		state: st, mode: st.Session.Mode, model: st.Session.Model,
		effort: st.Session.Effort, cwd: st.Cwd,
		theme: themeByName("conclave-dark"), themeName: "conclave-dark",
		input: ta, spin: sp, showSidebar: true, showToolDetail: true,
		streamIdx: map[string]int{}, toolIdx: map[string]int{},
		agents: map[string]*agentInfo{}, status: "ready", usageTab: 0,
	}
	m.chatVP = viewport.New(80, 20)
	m.dashVP = viewport.New(80, 20)
	if m.mode == domain.ModeConclave {
		m.refreshConclave()
		m.loadConclaveMessages()
		if m.run != nil && m.run.Run != nil && m.run.Run.Phase == domain.RunRunning {
			m.tab = 1
		}
	} else {
		m.usage = st.Session.Ledger
		m.loadChat()
	}
	if m.mode == domain.ModeConclave {
		m.usageKey = refKey(m.model)
	} else {
		m.usageKey = modelKey(st.Model.Provider, st.Model.ID)
	}
	m.status = "ready · /help"
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.waitEvent(), m.spin.Tick)
}

func (m *Model) waitEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.events
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
	case tea.KeyMsg:
		if m.modal != nil {
			return m, m.handleModalKey(msg)
		}
		if m.paletteVisible() {
			if cmd, handled := m.handlePaletteKey(msg); handled {
				return m, cmd
			}
		}
		switch msg.String() {
		case "ctrl+c":
			if m.busy {
				if m.cancel != nil {
					m.cancel()
				}
				m.status = "cancelling…"
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+d":
			return m, tea.Quit
		}
		if m.screen == 1 && m.paletteVisible() == false && m.input.Value() == "" {
			switch msg.String() {
			case "up", "down", "left", "right":
				m.moveBoard(msg.String())
				return m, nil
			case "enter":
				if t := m.selectedBoardTask(); t != nil {
					m.openTaskDetail(t)
				}
				return m, nil
			case "n":
				m.openTaskForm(nil)
				return m, nil
			case "e":
				if t := m.selectedBoardTask(); t != nil {
					m.openTaskForm(t)
				}
				return m, nil
			case "r":
				m.boardTransition(tracker.StatusReady)
				return m, nil
			case "v":
				m.boardTransition(tracker.StatusReview)
				return m, nil
			case "d":
				m.boardTransition(tracker.StatusDone)
				return m, nil
			case "o":
				m.boardTransition(tracker.StatusReady)
				return m, nil
			case "b":
				m.boardTransition(tracker.StatusBlocked)
				return m, nil
			case "x":
				m.boardTransition(tracker.StatusCancelled)
				return m, nil
			case "c":
				if m.app.Queue().Running() {
					m.app.PauseQueue()
					m.toast("board queue paused")
				} else if err := m.app.StartQueue(context.Background()); err != nil {
					m.toast("queue: " + err.Error())
				} else {
					m.toast("board queue started")
				}
				m.rebuild()
				return m, nil
			case "p":
				m.bumpPriority(1)
				return m, nil
			case "P":
				m.bumpPriority(-1)
				return m, nil
			case "esc":
				if m.boardSearch != "" {
					m.boardSearch = ""
					m.refreshBoard()
					m.rebuild()
				} else {
					m.screen = 0
					m.input.Placeholder = "Ask Conclave anything…  / commands   @ files"
					m.loadChat()
					m.layout()
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+p":
			m.paletteForced = true
			m.paletteSel = 0
			m.layout()
			return m, nil
		case "ctrl+u":
			m.openUsageModal()
			return m, nil
		case "?":
			if m.input.Value() == "" {
				m.modal = &modalState{kind: modalInfo, title: "Help", body: helpText}
				return m, nil
			}
		case "ctrl+b":
			m.showSidebar = !m.showSidebar
			m.layout()
			return m, nil
		case "ctrl+t":
			m.screen = 1 - m.screen
			if m.screen == 1 {
				m.refreshBoard()
				m.input.Placeholder = "Search tasks…  / commands"
				m.toast("project board")
			} else {
				m.input.Placeholder = "Ask Conclave anything…  / commands   @ files"
				m.loadChat()
				m.rebuild()
			}
			m.layout()
			return m, nil
		case "tab":
			if m.mode == domain.ModeConclave {
				m.tab = (m.tab + 1) % 3
				m.detail = false
				m.rebuild()
				return m, nil
			}
		case "shift+tab":
			if m.mode == domain.ModeConclave {
				m.tab--
				if m.tab < 0 {
					m.tab = 2
				}
				m.detail = false
				m.rebuild()
				return m, nil
			}
		case "esc":
			if m.detail {
				m.detail = false
				m.rebuild()
				return m, nil
			}
		case "pgup":
			m.activeViewport().PageUp()
			return m, nil
		case "pgdown":
			m.activeViewport().PageDown()
			return m, nil
		case "enter":
			if m.screen == 1 {
				text := strings.TrimSpace(m.input.Value())
				if text != "" {
					m.boardSearch = text
					m.input.Reset()
					m.refreshBoard()
					m.toast("search: " + text)
					m.rebuild()
				}
				return m, nil
			}
			if m.mode == domain.ModeConclave && m.tab == 1 && m.input.Value() == "" {
				m.detail = !m.detail
				m.rebuild()
				return m, nil
			}
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, nil
			}
			if m.busy {
				m.toast("agent is busy — ctrl+c to cancel")
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			return m, m.submit(text)
		case "alt+enter", "ctrl+j":
			m.input.InsertString("\n")
			return m, nil
		case "up", "down":
			if m.mode == domain.ModeConclave && m.tab == 1 && m.input.Value() == "" {
				m.moveTask(msg.String())
				return m, nil
			}
			if m.mode == domain.ModeConclave && m.tab == 2 && m.input.Value() == "" {
				m.moveAgent(msg.String())
				return m, nil
			}
			if m.input.Value() == "" && len(m.history) > 0 {
				if msg.String() == "up" {
					if m.histIdx > 0 {
						m.histIdx--
					}
					m.input.SetValue(m.history[m.histIdx])
				} else if m.histIdx < len(m.history)-1 {
					m.histIdx++
					m.input.SetValue(m.history[m.histIdx])
				}
				return m, nil
			}
		}
	case eventMsg:
		cmds = append(cmds, m.handleEvent(domain.Event(msg)), m.waitEvent())
	case sendDoneMsg:
		m.busy = false
		m.cancel = nil
		m.phase = ""
		m.refreshSession()
		m.refreshConclave()
		if msg.err != nil {
			m.errText = msg.err.Error()
			m.status = "error"
			m.toast(msg.err.Error())
		} else {
			m.errText = ""
			m.status = "ready"
		}
		m.rebuild()
	case modelsLoadedMsg:
		if m.modal != nil && m.modal.kind == modalLoading && m.modal.provider == msg.provider {
			m.openModelsFromResult(msg)
		}
	case actionMsg:
		m.busy = false
		m.phase = ""
		if msg.err != nil {
			m.toast(msg.err.Error())
			m.status = "error"
		} else if msg.text != "" {
			m.toast(msg.text)
			m.status = msg.text
		}
		m.refreshSession()
		m.refreshConclave()
		m.rebuild()
	case spinner.TickMsg:
		if m.busy {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			cmds = append(cmds, cmd)
			m.rebuild()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() != "up" && key.String() != "down" {
			m.paletteSel = 0
		}
		if m.ready {
			m.layout()
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) submit(text string) tea.Cmd {
	if strings.HasPrefix(text, "/") {
		name, arg := parseSlash(text)
		return m.executeCommand(name, arg)
	}
	if strings.HasPrefix(text, "!") {
		m.toast("shell shortcuts are not wired yet; ask the agent to run the command")
		m.input.SetValue(text)
		return nil
	}
	m.history = append(m.history, text)
	m.histIdx = len(m.history)
	m.busy = true
	m.status = "thinking…"
	m.phase = "waiting for model…"
	m.errText = ""
	m.chat = append(m.chat, chatEntry{kind: "user", content: text, created: time.Now()})
	m.rebuild()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	cmd := func() tea.Msg {
		return sendDoneMsg{err: m.app.Send(ctx, text)}
	}
	return tea.Batch(cmd, m.spin.Tick)
}

func parseSlash(s string) (string, string) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "/")
	name, arg, _ := strings.Cut(s, " ")
	return name, strings.TrimSpace(arg)
}

func (m *Model) toast(text string) {
	m.toastText = text
	m.toastAt = time.Now()
}

func (m *Model) handleEvent(ev domain.Event) tea.Cmd {
	chatRelevant := ev.AgentID == "main" || ev.AgentID == "master" || ev.AgentID == "master-review" || ev.AgentID == ""
	switch ev.Type {
	case "assistant.started":
		m.phase = "thinking…"
		if chatRelevant {
			idx := len(m.chat)
			m.chat = append(m.chat, chatEntry{kind: "assistant", agentID: ev.AgentID, streaming: true, created: time.Now()})
			m.streamIdx[ev.AgentID] = idx
		}
	case "assistant.reasoning":
		m.phase = "thinking…"
	case "assistant.delta":
		m.phase = "streaming…"
		if idx, ok := m.streamIdx[ev.AgentID]; ok && chatRelevant {
			m.chat[idx].content += ev.Text
		}
	case "assistant.completed":
		if msg, ok := ev.Payload.(domain.Message); ok && chatRelevant {
			if idx, exists := m.streamIdx[ev.AgentID]; exists {
				m.chat[idx].content = msg.Content
				m.chat[idx].streaming = false
			}
		}
		if chatRelevant {
			m.status = "working…"
			if m.phase == "" {
				m.phase = "thinking…"
			}
		}
	case "tool.started":
		m.phase = "running a tool…"
		payload, _ := ev.Payload.(map[string]any)
		call, _ := payload["call"].(domain.ToolCall)
		if call.Name != "" {
			m.phase = "running " + call.Name + "…"
		}
		if chatRelevant {
			idx := len(m.chat)
			m.chat = append(m.chat, chatEntry{
				kind: "tool", agentID: ev.AgentID, title: call.Name,
				content: truncate(call.Arguments, 2000), created: time.Now(),
			})
			m.toolIdx[call.ID] = idx
		} else if a := m.agents[ev.AgentID]; a != nil {
			a.Last = call.Name + " " + truncate(strings.ReplaceAll(call.Arguments, "\n", " "), 100)
			a.Tools = append(a.Tools, call.Name)
			a.Events = appendCapped(a.Events, "→ "+call.Name+" "+truncate(call.Arguments, 120), 100)
		}
	case "tool.completed", "tool.failed":
		payload, _ := ev.Payload.(map[string]any)
		id, _ := payload["call_id"].(string)
		if idx, ok := m.toolIdx[id]; ok {
			m.chat[idx].content = truncate(ev.Text, 12000)
			m.chat[idx].failed = ev.Type == "tool.failed"
			if ev.Type == "tool.failed" {
				m.chat[idx].title += " · failed"
			}
		}
		if a := m.agents[ev.AgentID]; a != nil {
			state := "✓"
			if ev.Type == "tool.failed" {
				state = "✖"
			}
			a.Events = appendCapped(a.Events, state+" "+truncate(ev.Text, 160), 100)
		}
	case "usage.updated":
		if rec, ok := ev.Payload.(domain.UsageRecord); ok {
			m.usage.Add(rec)
			if rec.LastContext > 0 && (m.mode != domain.ModeConclave || rec.AgentID == "master" || rec.AgentID == "master-review" || m.usageKey == "") {
				m.usageKey = modelKey(rec.Provider, rec.Model)
			}
		}
	case "agent.started":
		payload, _ := ev.Payload.(map[string]any)
		id, _ := payload["agent_id"].(string)
		modelRef, _ := payload["model"].(string)
		taskID, _ := payload["task_id"].(string)
		if id != "" && id != "main" && id != "master" && id != "master-review" {
			m.agents[id] = &agentInfo{ID: id, Model: modelRef, TaskID: taskID, Status: "running", Started: time.Now()}
		}
	case "agent.finished":
		if a := m.agents[ev.AgentID]; a != nil {
			a.Status = "finished"
			a.Events = appendCapped(a.Events, "finished", 100)
		}
	case "agent.error":
		if a := m.agents[ev.AgentID]; a != nil {
			a.Status = "failed"
			a.Last = ev.Text
			a.Events = appendCapped(a.Events, "error: "+ev.Text, 100)
		}
	case "run.phase_changed", "task.started", "task.finished", "merge.completed", "merge.conflict", "plan.ready", "plan.approved":
		m.refreshConclave()
		if ev.Type == "plan.ready" {
			m.status = "plan ready · /approve to dispatch workers"
			m.tab = 1
			m.toast("master submitted a plan")
		}
		if ev.Type == "task.finished" {
			payload, _ := ev.Payload.(map[string]any)
			if task, ok := payload["task"].(domain.Task); ok {
				m.toast(fmt.Sprintf("task %s: %s", task.ID, task.Status))
			}
		}
		if ev.Type == "run.phase_changed" {
			payload, _ := ev.Payload.(map[string]any)
			if p, ok := payload["phase"].(domain.RunPhase); ok {
				switch p {
				case domain.RunRunning:
					m.tab = 1
					m.status = "workers running"
				case domain.RunCompleted:
					m.phase = ""
					m.status = "run complete · /apply to merge"
					m.toast("Conclave run complete")
				case domain.RunPaused, domain.RunCancelled:
					m.phase = ""
				case domain.RunFailed:
					m.phase = ""
					m.toast("Conclave run failed")
				}
			}
		}
	case "board.changed":
		if m.screen == 1 {
			m.refreshBoard()
		}
		m.toast(ev.Text)
		m.rebuild()
	case "job.finished":
		payload, _ := ev.Payload.(map[string]any)
		id, _ := payload["id"].(string)
		status, _ := payload["status"].(string)
		m.toast(fmt.Sprintf("background %s %s", id, status))
	case "run.applied":
		m.toast("integration branch applied")
	case "run.error", "session.error":
		m.errText = ev.Text
		m.status = "error"
		m.toast(ev.Text)
	}
	m.rebuild()
	return nil
}

func (m *Model) refreshSession() {
	m.state = m.app.State()
	m.mode = m.state.Session.Mode
	m.cwd = m.state.Cwd
	if m.mode != domain.ModeConclave {
		m.model = m.state.Session.Model
		m.effort = m.state.Session.Effort
		m.planOnly = m.state.Session.PlanOnly
		m.usage = m.state.Session.Ledger
		if m.usageKey == "" {
			m.usageKey = modelKey(m.state.Model.Provider, m.state.Model.ID)
		}
	}
}

func (m *Model) refreshConclave() {
	if m.mode != domain.ModeConclave {
		return
	}
	s := m.app.Conclave().Snapshot()
	m.run = &s
	if s.MasterModel != "" {
		m.model = s.MasterModel
	}
	if s.MasterEffort != "" {
		m.effort = s.MasterEffort
	}
	if s.Run != nil {
		m.mergeLedger(s.Run.Ledger)
		if m.selTask >= len(s.Run.Tasks) {
			m.selTask = 0
		}
	}
	if s.MasterModel != "" {
		if _, ok := m.usage.ByModel[m.usageKey]; !ok {
			m.usageKey = refKey(s.MasterModel)
		}
	}
}

func (m *Model) mergeLedger(other domain.UsageLedger) {
	for k, rec := range other.ByModel {
		cur, ok := m.usage.ByModel[k]
		if !ok || rec.InputTokens >= cur.InputTokens {
			if m.usage.ByModel == nil {
				m.usage.ByModel = map[string]domain.UsageRecord{}
			}
			m.usage.ByModel[k] = rec
		}
	}
	for k, rec := range other.ByAgent {
		cur, ok := m.usage.ByAgent[k]
		if !ok || rec.InputTokens >= cur.InputTokens {
			if m.usage.ByAgent == nil {
				m.usage.ByAgent = map[string]domain.UsageRecord{}
			}
			m.usage.ByAgent[k] = rec
		}
	}
}

func (m *Model) loadChat() {
	m.chat = nil
	for _, msg := range m.state.Session.Messages {
		switch msg.Role {
		case domain.RoleUser:
			m.chat = append(m.chat, chatEntry{kind: "user", content: msg.Content, created: msg.CreatedAt})
		case domain.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				m.chat = append(m.chat, chatEntry{kind: "assistant", content: msg.Content, created: msg.CreatedAt})
			}
		}
	}
}

func (m *Model) loadConclaveMessages() {
	m.chat = nil
	if m.run == nil || m.run.Run == nil {
		return
	}
	for _, msg := range m.run.Messages {
		if msg.Role == domain.RoleAssistant && strings.TrimSpace(msg.Content) != "" {
			m.chat = append(m.chat, chatEntry{kind: "assistant", content: msg.Content, created: msg.CreatedAt})
		}
	}
}

func (m *Model) activeViewport() *viewport.Model {
	if m.mode == domain.ModeConclave && m.tab != 0 {
		return &m.dashVP
	}
	return &m.chatVP
}

func (m *Model) moveTask(dir string) {
	if m.run == nil || m.run.Run == nil || len(m.run.Run.Tasks) == 0 {
		return
	}
	n := len(m.run.Run.Tasks)
	if dir == "up" {
		m.selTask = (m.selTask - 1 + n) % n
	} else {
		m.selTask = (m.selTask + 1) % n
	}
	m.rebuild()
}

func (m *Model) moveAgent(dir string) {
	list := m.sortedAgents()
	if len(list) == 0 {
		return
	}
	if dir == "up" {
		m.selAgent = (m.selAgent - 1 + len(list)) % len(list)
	} else {
		m.selAgent = (m.selAgent + 1) % len(list)
	}
	m.rebuild()
}

func (m *Model) sortedAgents() []*agentInfo {
	list := make([]*agentInfo, 0, len(m.agents))
	for _, a := range m.agents {
		list = append(list, a)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Started.Before(list[j].Started) })
	if m.selAgent >= len(list) {
		m.selAgent = len(list) - 1
	}
	if m.selAgent < 0 {
		m.selAgent = 0
	}
	return list
}

func (m *Model) openProviderPicker() {
	providers := m.app.Providers()
	items := make([]modalItem, 0, len(providers)+1)
	sel := 0
	for _, p := range providers {
		n := len(m.app.ProviderModels(p))
		items = append(items, modalItem{
			Label: p, Value: p, Provider: p,
			Description: fmt.Sprintf("%d configured models", n),
		})
		if strings.HasPrefix(m.model, p+"/") {
			sel = len(items) - 1
		}
	}
	items = append(items, modalItem{Label: "＋ Add provider…", Value: "__add__", Description: "base URL, API key and models"})
	m.modal = &modalState{kind: modalProviders, title: "Providers", items: items, sel: sel}
}

func (m *Model) openAddProvider() {
	m.modal = &modalState{
		kind: modalAddProvider, title: "Add provider",
		form: providerForm{URL: "https://openrouter.ai/api/v1"},
	}
}

func (m *Model) startModelFetch(provider string) tea.Cmd {
	m.modal = &modalState{kind: modalLoading, title: "Models", provider: provider, body: "Fetching models from " + provider + "…"}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		models, err := m.app.FetchModels(ctx, provider)
		return modelsLoadedMsg{provider: provider, models: models, err: err}
	}
}

func (m *Model) openModelsFromResult(msg modelsLoadedMsg) {
	configured := m.app.ProviderModels(msg.provider)
	seen := map[string]bool{}
	var items []modalItem
	for _, rm := range msg.models {
		seen[rm.ID] = true
		items = append(items, modalItem{
			Label: rm.ID, Value: msg.provider + "/" + rm.ID,
			Provider: msg.provider, ModelID: rm.ID,
			Context: rm.Context, MaxOutput: rm.MaxOutput,
			Description: fmt.Sprintf("ctx %s", tokens(rm.Context)),
		})
	}
	for _, id := range configured {
		if seen[id] {
			continue
		}
		items = append(items, modalItem{
			Label: id, Value: msg.provider + "/" + id,
			Provider: msg.provider, ModelID: id,
			Context: 128000, MaxOutput: 8192,
			Description: "configured",
		})
	}
	if len(items) == 0 {
		errText := "no models returned"
		if msg.err != nil {
			errText = msg.err.Error()
		}
		m.modal = &modalState{kind: modalInfo, title: "Models", body: "Could not load models:\n\n" + errText}
		return
	}
	if msg.err != nil {
		m.toast("model list: " + msg.err.Error())
	}
	sel := 0
	for i, it := range items {
		if it.Value == m.model {
			sel = i
		}
	}
	m.modal = &modalState{kind: modalModels, title: msg.provider + " models", items: items, sel: sel, provider: msg.provider}
}

func (m *Model) selectModel(it modalItem) {
	if err := m.app.EnsureModel(it.Provider, it.ModelID, it.Context, it.MaxOutput); err != nil {
		m.toast("model: " + err.Error())
		return
	}
	if err := m.app.SetModel(it.Value); err != nil {
		m.toast("model: " + err.Error())
		return
	}
	m.toast("model: " + it.Value)
	m.refreshSession()
	m.usageKey = modelKey(it.Provider, it.ModelID)
	m.modal = nil
	m.rebuild()
}

func (m *Model) handleProviderForm(msg tea.KeyMsg) tea.Cmd {
	md := m.modal
	f := &md.form
	switch msg.String() {
	case "tab", "down":
		f.Focus = (f.Focus + 1) % 3
		return nil
	case "shift+tab", "up":
		f.Focus--
		if f.Focus < 0 {
			f.Focus = 2
		}
		return nil
	case "enter":
		if f.Focus < 2 {
			f.Focus++
			return nil
		}
		if strings.TrimSpace(f.Name) == "" || strings.TrimSpace(f.URL) == "" {
			m.toast("provider name and base URL are required")
			return nil
		}
		if err := m.app.AddProvider(f.Name, f.URL, f.Key); err != nil {
			m.toast("provider: " + err.Error())
			return nil
		}
		provider := strings.ToLower(strings.TrimSpace(f.Name))
		m.toast("provider added: " + provider)
		return m.startModelFetch(provider)
	case "backspace":
		field := formField(f)
		if rs := []rune(*field); len(rs) > 0 {
			*field = string(rs[:len(rs)-1])
		}
		return nil
	case "ctrl+u":
		*formField(f) = ""
		return nil
	case "space", " ":
		*formField(f) += " "
		return nil
	}
	if msg.Type == tea.KeyRunes {
		*formField(f) += string(msg.Runes)
	}
	return nil
}

func formField(f *providerForm) *string {
	switch f.Focus {
	case 1:
		return &f.URL
	case 2:
		return &f.Key
	default:
		return &f.Name
	}
}

func (m *Model) openThemePicker() {
	items := make([]modalItem, 0, len(themes))
	sel := 0
	for i, t := range themes {
		items = append(items, modalItem{Label: t.Name, Value: t.Name, Description: themeDescription(t.Name)})
		if t.Name == m.themeName {
			sel = i
		}
	}
	m.modal = &modalState{kind: modalThemes, title: "Choose theme", items: items, sel: sel}
}

func (m *Model) openSessionsModal() {
	list, err := m.app.Sessions(context.Background())
	if err != nil {
		m.toast("sessions: " + err.Error())
		return
	}
	items := make([]modalItem, 0, len(list))
	sel := 0
	for i, s := range list {
		desc := string(s.Mode) + " · " + s.UpdatedAt.Format("02 Jan 15:04")
		if s.Active {
			desc = "active · " + desc
			sel = i
		}
		items = append(items, modalItem{Label: s.Title, Value: s.ID, Description: desc})
	}
	m.modal = &modalState{kind: modalSessions, title: "Sessions", items: items, sel: sel}
}

func (m *Model) openUsageModal() {
	m.modal = &modalState{kind: modalUsage, title: "Usage", sel: m.usageSel, usageTab: m.usageTab}
}

func (m *Model) handleModalKey(msg tea.KeyMsg) tea.Cmd {
	md := m.modal
	if md.kind == modalTaskForm {
		return m.handleTaskForm(msg)
	}
	if md.kind == modalAddProvider {
		if msg.String() == "esc" {
			m.modal = nil
			return nil
		}
		return m.handleProviderForm(msg)
	}
	if msg.String() == "esc" || msg.String() == "q" {
		m.modal = nil
		return nil
	}
	switch msg.String() {
	case "up":
		if md.sel > 0 {
			md.sel--
		}
		return nil
	case "down":
		if md.kind == modalUsage {
			n := m.usageItemCount()
			if md.sel < n-1 {
				md.sel++
			}
		} else if md.sel < len(md.items)-1 {
			md.sel++
		}
		return nil
	case "tab":
		if md.kind == modalUsage && m.mode == domain.ModeConclave {
			md.usageTab = (md.usageTab + 1) % 2
			md.sel = 0
		}
		return nil
	case "enter":
		switch md.kind {
		case modalModels:
			if md.sel >= 0 && md.sel < len(md.items) {
				m.selectModel(md.items[md.sel])
			}
		case modalSessions:
			if md.sel >= 0 && md.sel < len(md.items) {
				if err := m.app.OpenSession(context.Background(), md.items[md.sel].Value); err != nil {
					m.toast("session: " + err.Error())
					return nil
				}
				m.modal = nil
				m.refreshSession()
				m.refreshConclave()
				m.loadChat()
				m.usage = m.state.Session.Ledger
				m.toast("session opened")
				m.rebuild()
			}
		case modalProviders:
			if md.sel >= 0 && md.sel < len(md.items) {
				it := md.items[md.sel]
				if it.Value == "__add__" {
					m.openAddProvider()
					return nil
				}
				return m.startModelFetch(it.Provider)
			}
		case modalThemes:
			if md.sel >= 0 && md.sel < len(md.items) {
				m.applyTheme(md.items[md.sel].Value)
				m.modal = nil
			}
		case modalUsage:
			m.usageSel = md.sel
			m.usageTab = md.usageTab
			m.modal = nil
		default:
			m.modal = nil
		}
		return nil
	}
	if md.kind == modalUsage {
		m.usageSel = md.sel
		m.usageTab = md.usageTab
	}
	return nil
}

func (m *Model) usageItemCount() int {
	if m.modal != nil && m.modal.usageTab == 1 {
		return len(m.usage.Agents())
	}
	return len(m.usage.Models())
}

func (m *Model) paletteVisible() bool {
	if m.modal != nil {
		return false
	}
	v := m.input.Value()
	return m.paletteForced || (strings.HasPrefix(v, "/") && !strings.Contains(v, "\n"))
}

func (m *Model) paletteItems() []paletteItem {
	v := m.input.Value()
	forced := m.paletteForced && !strings.HasPrefix(v, "/")
	raw := strings.TrimPrefix(v, "/")
	raw = strings.TrimRight(raw, "\n")
	if forced {
		raw = strings.TrimSpace(v)
	}
	if !strings.Contains(raw, " ") {
		prefix := strings.TrimSpace(raw)
		var out []paletteItem
		for _, c := range commandSpecs() {
			if prefix != "" && !strings.HasPrefix(c.Name, prefix) && !(c.Alias != "" && strings.HasPrefix(c.Alias, prefix)) {
				continue
			}
			out = append(out, paletteItem{
				Label: "/" + c.Name, Description: c.Description, Spec: c,
			})
		}
		return out
	}
	name, arg, _ := strings.Cut(raw, " ")
	name = strings.TrimSpace(name)
	arg = strings.TrimSpace(arg)
	spec, ok := findCommand(name)
	if !ok || spec.Arg == argNone {
		return nil
	}
	var out []paletteItem
	for _, value := range m.completionValues(spec) {
		if arg != "" && !strings.HasPrefix(strings.ToLower(value), strings.ToLower(arg)) {
			continue
		}
		out = append(out, paletteItem{
			Label: value, Value: value, Description: "argument for /" + spec.Name, Spec: spec, IsArg: true,
		})
	}
	return out
}

func (m *Model) handlePaletteKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	items := m.paletteItems()
	switch msg.String() {
	case "up":
		if m.paletteSel > 0 {
			m.paletteSel--
		}
		return nil, true
	case "down":
		if m.paletteSel < len(items)-1 {
			m.paletteSel++
		}
		return nil, true
	case "esc":
		m.paletteForced = false
		m.input.Reset()
		m.paletteSel = 0
		m.layout()
		return nil, true
	case "tab", "enter":
		if len(items) == 0 {
			return nil, false
		}
		if m.paletteSel >= len(items) {
			m.paletteSel = 0
		}
		it := items[m.paletteSel]
		if it.IsArg {
			m.input.Reset()
			m.paletteForced = false
			m.paletteSel = 0
			cmd := m.executeCommand(it.Spec.Name, it.Value)
			m.layout()
			return cmd, true
		}
		if it.Spec.Arg != argNone && !it.Spec.Optional {
			m.input.SetValue("/" + it.Spec.Name + " ")
			m.paletteForced = false
			m.paletteSel = 0
			m.layout()
			return nil, true
		}
		m.input.Reset()
		m.paletteForced = false
		m.paletteSel = 0
		cmd := m.executeCommand(it.Spec.Name, "")
		m.layout()
		return cmd, true
	}
	return nil, false
}
