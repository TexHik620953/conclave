package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/texhik/conclave/internal/domain"
)

type argKind int

const (
	argNone argKind = iota
	argModel
	argMode
	argEffort
	argTheme
	argDir
	argQueue
)

type commandSpec struct {
	Name        string
	Description string
	Arg         argKind
	Optional    bool
	Alias       string
}

func commandSpecs() []commandSpec {
	return []commandSpec{
		{Name: "models", Description: "providers and models", Alias: "model"},
		{Name: "mode", Description: "switch Build / Conclave", Arg: argMode},
		{Name: "plan", Description: "toggle read-only Plan mode"},
		{Name: "effort", Description: "set reasoning effort", Arg: argEffort},
		{Name: "theme", Description: "switch the TUI theme", Arg: argTheme, Optional: true},
		{Name: "cd", Description: "change workspace", Arg: argDir},
		{Name: "board", Description: "project task board"},
		{Name: "newtask", Description: "create a project task", Alias: "task"},
		{Name: "queue", Description: "start/pause board queue", Arg: argQueue, Optional: true},
		{Name: "usage", Description: "tokens and context by model/agent"},
		{Name: "context", Description: "show context and token usage"},
		{Name: "connect", Description: "how to add a provider and API key"},
		{Name: "sessions", Description: "switch session"},
		{Name: "new", Description: "start a new session", Alias: "clear"},
		{Name: "compact", Description: "summarize and compact context", Alias: "summarize"},
		{Name: "details", Description: "toggle tool output details"},
		{Name: "tasks", Description: "Conclave task board"},
		{Name: "agents", Description: "Conclave worker lanes"},
		{Name: "chat", Description: "Conclave master chat"},
		{Name: "approve", Description: "approve the Conclave plan"},
		{Name: "pause", Description: "pause the Conclave run"},
		{Name: "resume", Description: "resume the Conclave run"},
		{Name: "cancel", Description: "cancel the Conclave run"},
		{Name: "apply", Description: "apply the integration branch"},
		{Name: "help", Description: "show commands and keys"},
		{Name: "quit", Description: "exit Conclave", Alias: "exit"},
	}
}

func findCommand(name string) (commandSpec, bool) {
	for _, c := range commandSpecs() {
		if c.Name == name || (c.Alias != "" && c.Alias == name) {
			return c, true
		}
	}
	return commandSpec{}, false
}

func (m *Model) completionValues(c commandSpec) []string {
	switch c.Arg {
	case argModel:
		v := m.app.Models()
		sort.Strings(v)
		return v
	case argMode:
		return []string{"build", "conclave"}
	case argEffort:
		return []string{"low", "medium", "high", "ultra"}
	case argQueue:
		return []string{"start", "pause", "status"}
	case argTheme:
		out := make([]string, 0, len(themes))
		for _, t := range themes {
			out = append(out, t.Name)
		}
		return out
	case argDir:
		entries, err := os.ReadDir(m.cwd)
		if err != nil {
			return nil
		}
		var out []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				out = append(out, e.Name()+string(filepath.Separator))
			}
		}
		sort.Strings(out)
		return out
	default:
		return nil
	}
}

// executeCommand runs a command. Commands that start asynchronous work return
// a tea.Cmd; all state-only commands update the model directly.
func (m *Model) executeCommand(name, arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	spec, ok := findCommand(name)
	if !ok {
		m.toast("unknown command /" + name)
		return nil
	}
	m.history = append(m.history, "/"+spec.Name+argSuffix(arg))
	switch spec.Name {
	case "models":
		m.openProviderPicker()
	case "mode":
		mode := m.mode
		if arg == "" {
			if m.mode == domain.ModeBuild {
				mode = domain.ModeConclave
			} else {
				mode = domain.ModeBuild
			}
		} else {
			mode = domain.Mode(arg)
		}
		planAlias := strings.EqualFold(arg, "plan")
		if planAlias {
			mode = domain.ModeBuild
		}
		if err := m.app.SetMode(mode); err != nil {
			m.toast("mode: " + err.Error())
		} else {
			if planAlias {
				_ = m.app.SetPlanOnly(true)
			}
			m.mode = mode
			m.tab = 0
			m.detail = false
			m.refreshSession()
			if mode == domain.ModeConclave {
				m.usage = domain.UsageLedger{}
				m.usageKey = ""
				m.refreshConclave()
				m.loadConclaveMessages()
				m.toast("Conclave: talk to the master, then /approve")
			} else {
				m.usage = m.state.Session.Ledger
				m.loadChat()
				m.toast("mode: " + string(mode))
			}
			m.rebuild()
		}
	case "effort":
		if arg == "" {
			m.toast("effort: " + m.effort + " (low|medium|high|ultra)")
			return nil
		}
		if err := m.app.SetEffort(arg); err != nil {
			m.toast("effort: " + err.Error())
		} else {
			m.effort = arg
			m.toast("effort: " + arg)
		}
	case "theme":
		if arg == "" {
			m.openThemePicker()
			return nil
		}
		m.applyTheme(arg)
	case "cd":
		if arg == "" {
			m.toast("cwd: " + m.cwd)
			return nil
		}
		if err := m.app.Chdir(arg); err != nil {
			m.toast("cd: " + err.Error())
		} else {
			m.refreshSession()
			m.refreshConclave()
			m.usage = domain.UsageLedger{}
			if m.mode == domain.ModeConclave {
				m.loadConclaveMessages()
			} else {
				m.loadChat()
			}
			m.toast("cwd: " + m.cwd)
			m.rebuild()
		}
	case "board":
		m.screen = 1
		m.refreshBoard()
		m.toast("project board")
		m.rebuild()
	case "newtask":
		m.openTaskForm(nil)
	case "queue":
		switch arg {
		case "start", "run":
			if err := m.app.StartQueue(context.Background()); err != nil {
				m.toast("queue: " + err.Error())
			} else {
				m.toast("board queue started")
			}
		case "pause", "stop":
			m.app.PauseQueue()
			m.toast("board queue paused")
		default:
			st := m.app.Queue().Status()
			m.toast(fmt.Sprintf("queue running=%v active=%d", st.Running, st.Active))
		}
		m.refreshBoard()
	case "usage", "context":
		m.openUsageModal()
	case "connect":
		m.modal = &modalState{kind: modalInfo, title: "Connect a provider", body: connectHelp}
	case "sessions":
		m.openSessionsModal()
	case "new":
		if err := m.app.NewSession(); err != nil {
			m.toast("new: " + err.Error())
		} else {
			m.chat = nil
			m.streamIdx = map[string]int{}
			m.toolIdx = map[string]int{}
			m.agents = map[string]*agentInfo{}
			m.usage = domain.UsageLedger{}
			m.refreshSession()
			m.refreshConclave()
			m.loadChat()
			m.toast("new session")
			m.rebuild()
		}
	case "compact":
		m.busy = true
		m.status = "compacting context…"
		return func() tea.Msg {
			return actionMsg{err: m.app.Compact(context.Background()), text: "context compacted"}
		}
	case "details":
		m.showToolDetail = !m.showToolDetail
		m.toast(fmt.Sprintf("tool details: %v", m.showToolDetail))
		m.rebuild()
	case "tasks":
		if m.mode == domain.ModeConclave {
			m.tab = 1
			m.detail = false
			m.rebuild()
		}
	case "agents":
		if m.mode == domain.ModeConclave {
			m.tab = 2
			m.detail = false
			m.rebuild()
		}
	case "chat":
		m.tab = 0
		m.rebuild()
	case "approve":
		if m.mode != domain.ModeConclave {
			m.toast("approval is only available in Conclave")
			return nil
		}
		if err := m.app.Conclave().Approve(context.Background()); err != nil {
			m.toast("approve: " + err.Error())
		} else {
			m.toast("workers dispatched")
			m.tab = 1
			m.refreshConclave()
			m.rebuild()
		}
	case "pause":
		if err := m.app.Conclave().Pause(); err != nil {
			m.toast("pause: " + err.Error())
		} else {
			m.toast("run paused")
			m.refreshConclave()
		}
	case "resume":
		if err := m.app.Conclave().Resume(context.Background()); err != nil {
			m.toast("resume: " + err.Error())
		} else {
			m.toast("run resumed")
			m.refreshConclave()
		}
	case "cancel":
		if err := m.app.Conclave().Cancel(); err != nil {
			m.toast("cancel: " + err.Error())
		} else {
			m.toast("run cancelled")
			m.refreshConclave()
		}
	case "apply":
		if err := m.app.Conclave().Apply(context.Background()); err != nil {
			m.toast("apply: " + err.Error())
		} else {
			m.toast("integration branch applied")
		}
	case "plan":
		if m.mode == domain.ModeConclave {
			m.toast("Conclave master is already read-only")
			return nil
		}
		next := !m.planOnly
		if err := m.app.SetPlanOnly(next); err != nil {
			m.toast("plan: " + err.Error())
		} else {
			m.planOnly = next
			if next {
				m.toast("plan only: write tools disabled")
			} else {
				m.toast("act mode: write tools enabled")
			}
			m.refreshSession()
		}
		m.rebuild()
		return nil
	case "help":
		m.modal = &modalState{kind: modalInfo, title: "Help", body: helpText}
	case "quit", "exit":
		return tea.Quit
	}
	m.rebuild()
	return nil
}

func argSuffix(arg string) string {
	if arg == "" {
		return ""
	}
	return " " + arg
}

func (m *Model) applyTheme(name string) {
	t := themeByName(name)
	m.theme = t
	m.themeName = t.Name
	m.toast("theme: " + t.Name)
	m.rebuild()
}

const connectHelp = `Providers are configured in .conclave/config.yaml.

providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    max_parallel: 8
    models:
      deepseek/deepseek-chat:
        context: 128000
        max_output: 8192
        capabilities: {tools: true}

Then export the key:

  export OPENROUTER_API_KEY=sk-or-...

Use /models to choose a provider and a model.`

const helpText = `Commands
  /models                     choose provider and model
  /mode [build|conclave]        switch mode
  /plan                        toggle read-only Plan mode
  /effort [low|medium|high|ultra]
  /theme [name]                switch theme
  /cd [path]                   change workspace
  /board                       project task board
  /newtask                     create a project task
  /queue start|pause|status    board execution queue
  /usage                       tokens and context by model/agent
  /connect                     how to add a provider
  /board                       project task board
  /newtask                     create a project task
  /queue start|pause|status    run the board queue
  /new                         new session
  /compact                     compact context
  /details                     toggle tool output details
  /tasks /agents /chat         Conclave views

Conclave
  /approve                     start workers
  /pause /resume /cancel       control the run
  /apply                       merge integration branch

Keys
  Enter             send or execute
  Alt+Enter         newline
  /                 command menu
  Ctrl+P            command palette
  Tab / Shift+Tab   switch Conclave view
  Ctrl+B            toggle sidebar
  Ctrl+U            usage
  Ctrl+C            cancel current request / quit
  Ctrl+D            quit`
