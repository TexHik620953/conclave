package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/texhik/conclave/internal/app"
	"github.com/texhik/conclave/internal/conclave"
	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tracker"
)

func testApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "test")
	t.Setenv("OPENROUTER_API_KEY", "test")
	if _, err := config.Seed(dir); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	a, err := app.New(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func keyEnter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func TestSlashCommandsAndPalette(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.width, m.height, m.ready = 120, 40, true
	m.input.SetValue("/mod")
	items := m.paletteItems()
	if len(items) < 2 {
		t.Fatalf("palette items = %d, want model/mode", len(items))
	}
	// Palette executes the selected command.
	m.input.SetValue("/mode plan")
	m.Update(keyEnter())
	if got := a.State().Session.Mode; got != domain.ModeBuild {
		t.Fatalf("mode = %s, want build", got)
	}
	if !a.State().Session.PlanOnly {
		t.Fatal("plan alias did not enable plan_only")
	}
	m.input.SetValue("/usage")
	m.Update(keyEnter())
	if m.modal == nil || m.modal.kind != modalUsage {
		t.Fatalf("usage modal not opened: %#v", m.modal)
	}
	m.modal = nil
	m.input.SetValue("/models")
	m.Update(keyEnter())
	if m.modal == nil || m.modal.kind != modalProviders {
		t.Fatalf("provider picker not opened: %#v", m.modal)
	}
	if len(m.modal.items) < 3 {
		t.Fatalf("providers modal: %#v", m.modal.items)
	}
}

func TestUsageIsPerModelAndRendering(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.width, m.height, m.ready = 120, 40, true
	m.usage.Add(domain.UsageRecord{Provider: "deepseek", Model: "deepseek-chat", AgentID: "main", InputTokens: 12340, OutputTokens: 2100, LastContext: 12340, ContextWindow: 128000, Requests: 4})
	m.usage.Add(domain.UsageRecord{Provider: "openrouter", Model: "anthropic/claude", AgentID: "master", InputTokens: 5000, OutputTokens: 800, LastContext: 5000, ContextWindow: 200000, Requests: 2})
	m.usageKey = "deepseek/deepseek-chat"
	side := m.renderUsageSidebar(28, 20)
	if !contains(side, "deepseek-chat") || !contains(side, "12.3k") {
		t.Fatalf("sidebar:\n%s", side)
	}
	if contains(side, "$") {
		t.Fatalf("price leaked into usage: %s", side)
	}
	modal := m.renderUsageModal(&modalState{kind: modalUsage, sel: 0}, 70, 18)
	if !contains(modal, "deepseek") || !contains(modal, "openrouter") {
		t.Fatalf("usage modal:\n%s", modal)
	}
}

func TestThemeSwitch(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.applyTheme("tokyonight")
	if m.theme.Name != "tokyonight" {
		t.Fatalf("theme = %s", m.theme.Name)
	}
	if m.theme.Bg != "#1a1b26" {
		t.Fatalf("bg = %s", m.theme.Bg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestViewFitsAndShowsFooter(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.width, m.height, m.ready = 80, 28, true
	m.layout()
	v := m.View()
	if !contains(v, "commands · ctrl+p") {
		t.Fatalf("footer missing:\n%s", v)
	}
	if strings.Count(v, "\n")+1 > 28 {
		t.Fatalf("view is %d lines, want <= 28\n%s", strings.Count(v, "\n")+1, v)
	}
}

func TestPaletteArgumentsAndTaskDashboard(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.width, m.height, m.ready = 120, 40, true
	m.input.SetValue("/mode ")
	items := m.paletteItems()
	if len(items) != 2 || items[1].Value != "conclave" {
		t.Fatalf("mode arguments: %#v", items)
	}

	m.mode = domain.ModeConclave
	m.run = &conclave.Snapshot{
		Run: &domain.Run{
			Phase: domain.RunRunning, Goal: "build it", MaxIterations: 3,
			Tasks: []domain.Task{
				{ID: "t1", Title: "first", WorkerID: "coder", Status: domain.TaskDone, Commit: "abc", Summary: "done"},
				{ID: "t2", Title: "second", WorkerID: "coder", Status: domain.TaskRunning, Spec: "spec"},
			},
		},
		PendingPlan: &domain.Plan{Goal: "build it", Tasks: []domain.Task{{ID: "t3", Title: "next", WorkerID: "coder"}}},
	}
	out := m.renderTasks(80)
	if !contains(out, "PLAN AWAITING APPROVAL") || !contains(out, "t2") {
		t.Fatalf("tasks view:\n%s", out)
	}
	m.detail = true
	m.selTask = 1
	out = m.renderTasks(80)
	if !contains(out, "spec") || !contains(out, "t2") {
		t.Fatalf("task detail:\n%s", out)
	}
}

func TestTruncateIsUnicodeAndANSIAware(t *testing.T) {
	logo := "██████╗ ██████╗ C O N C L A V E"
	got := truncate(logo, 12)
	if !strings.Contains(got, "C") && !strings.Contains(got, "█") {
		t.Fatalf("unexpected truncation: %q", got)
	}
	if len([]rune(got)) == 0 {
		t.Fatal("truncation returned invalid text")
	}
	frame := fitFrame("one\ntwo\nthree", 6, 2)
	if strings.Count(frame, "\n") != 1 {
		t.Fatalf("frame has wrong height: %q", frame)
	}
}

func TestPaletteFrameHasNoArtifacts(t *testing.T) {
	a := testApp(t)
	for _, size := range [][2]int{{80, 28}, {120, 40}} {
		m := New(a)
		m.width, m.height, m.ready = size[0], size[1], true
		m.input.SetValue("/")
		m.layout()
		v := m.View()
		lines := strings.Split(v, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%dx%d: lines=%d", size[0], size[1], len(lines))
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w > size[0] {
				t.Fatalf("%dx%d line %d width=%d", size[0], size[1], i, w)
			}
		}
		if !contains(v, "/board") {
			t.Fatalf("%dx%d: palette missing", size[0], size[1])
		}
	}
}

func TestStreamingEventsAndNoRoleLabels(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.width, m.height, m.ready = 100, 30, true
	m.mode = domain.ModeBuild
	m.handleEvent(domain.Event{Type: "assistant.started", AgentID: "main"})
	m.handleEvent(domain.Event{Type: "assistant.delta", AgentID: "main", Text: "Hello "})
	m.handleEvent(domain.Event{Type: "assistant.delta", AgentID: "main", Text: "world"})
	if len(m.chat) == 0 || !contains(m.chat[0].content, "Hello world") {
		t.Fatalf("stream not accumulated: %#v", m.chat)
	}
	if m.phase != "streaming…" {
		t.Fatalf("phase = %q", m.phase)
	}
	view := m.renderChat(80)
	if contains(view, "You") || contains(view, "Assistant") {
		t.Fatalf("role labels leaked:\n%s", view)
	}
	if !contains(view, "Hello world") {
		t.Fatalf("streamed text missing:\n%s", view)
	}
}

func TestProviderFormAndModelResults(t *testing.T) {
	a := testApp(t)
	m := New(a)
	m.width, m.height, m.ready = 120, 40, true
	m.openAddProvider()
	m.handleModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("local")})
	if m.modal.form.Name != "local" {
		t.Fatalf("form name = %q", m.modal.form.Name)
	}
	m.openModelsFromResult(modelsLoadedMsg{provider: "fake", models: []llm.RemoteModel{{ID: "model-a", Context: 32000, MaxOutput: 2048}}})
	if m.modal == nil || m.modal.kind != modalModels || len(m.modal.items) == 0 {
		t.Fatalf("models modal = %#v", m.modal)
	}
	it := m.modal.items[0]
	if it.Provider != "fake" || it.ModelID != "model-a" || it.Context != 32000 {
		t.Fatalf("model item = %#v", it)
	}
}

func TestBoardCreateAndRender(t *testing.T) {
	a := testApp(t)
	_, err := a.Tracker().Create(context.Background(), tracker.CreateInput{
		Title: "board task", Description: "desc", Complexity: 3, Priority: 4,
		Status: tracker.StatusReady, CreatedBy: "user",
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	m := New(a)
	m.width, m.height, m.ready = 120, 40, true
	m.screen = 1
	m.refreshBoard()
	if len(m.boardTasks) != 1 {
		t.Fatalf("tasks = %d", len(m.boardTasks))
	}
	m.layout()
	v := m.View()
	if !contains(v, "board task") || !contains(v, "BOARD") {
		t.Fatalf("board view:\n%s", v)
	}
}
