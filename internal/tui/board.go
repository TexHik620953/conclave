package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/texhik/conclave/internal/tracker"
)

var boardStatuses = []tracker.TaskStatus{
	tracker.StatusBacklog,
	tracker.StatusReady,
	tracker.StatusInProgress,
	tracker.StatusReview,
	tracker.StatusDone,
}

func (m *Model) refreshBoard() {
	if m.app.Tracker() == nil {
		return
	}
	tasks, err := m.app.Tracker().List(context.Background(), tracker.Filter{Limit: 500, Search: m.boardSearch})
	if err != nil {
		m.toast("board: " + err.Error())
		return
	}
	m.boardTasks = tasks
	if m.boardCol < 0 || m.boardCol >= len(boardStatuses) {
		m.boardCol = 0
	}
	col := m.boardColumnTasks()
	if m.boardSel >= len(col) {
		m.boardSel = max(0, len(col)-1)
	}
}

func (m *Model) boardColumnTasks() []tracker.Task {
	if m.boardCol < 0 || m.boardCol >= len(boardStatuses) {
		return nil
	}
	status := boardStatuses[m.boardCol]
	var out []tracker.Task
	for _, t := range m.boardTasks {
		if t.Status == status {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) selectedBoardTask() *tracker.Task {
	col := m.boardColumnTasks()
	if m.boardSel < 0 || m.boardSel >= len(col) {
		return nil
	}
	t := col[m.boardSel]
	return &t
}

func (m *Model) moveBoard(dir string) {
	col := m.boardColumnTasks()
	switch dir {
	case "left":
		m.boardCol--
		if m.boardCol < 0 {
			m.boardCol = len(boardStatuses) - 1
		}
		m.boardSel = 0
	case "right":
		m.boardCol = (m.boardCol + 1) % len(boardStatuses)
		m.boardSel = 0
	case "up":
		if m.boardSel > 0 {
			m.boardSel--
		}
	case "down":
		if m.boardSel < len(col)-1 {
			m.boardSel++
		}
	}
	m.rebuild()
}

func (m *Model) openTaskForm(t *tracker.Task) {
	f := taskForm{Complexity: "3", Priority: "3"}
	if t != nil {
		f = taskForm{
			TaskID: t.ID, Title: t.Title, Description: t.Description, Context: t.Context,
			Complexity: strconv.Itoa(t.Complexity), Priority: strconv.Itoa(t.Priority),
			Component: t.Component, Labels: strings.Join(t.Labels, ", "), Epic: t.Epic,
		}
	}
	title := "New task"
	if t != nil {
		title = "Edit " + t.ID
	}
	m.modal = &modalState{kind: modalTaskForm, title: title, taskForm: f}
}

func (m *Model) taskFormFields(f *taskForm) []*string {
	return []*string{&f.Title, &f.Description, &f.Context, &f.Complexity, &f.Priority, &f.Component, &f.Labels, &f.Epic}
}

func (m *Model) handleTaskForm(msg tea.KeyMsg) tea.Cmd {
	md := m.modal
	f := &md.taskForm
	fields := m.taskFormFields(f)
	switch msg.String() {
	case "esc":
		m.modal = nil
		return nil
	case "tab", "down":
		f.Focus = (f.Focus + 1) % len(fields)
		return nil
	case "shift+tab", "up":
		f.Focus--
		if f.Focus < 0 {
			f.Focus = len(fields) - 1
		}
		return nil
	case "enter", "ctrl+s":
		if msg.String() == "enter" && f.Focus < len(fields)-1 {
			f.Focus++
			return nil
		}
		if err := m.saveTaskForm(); err != nil {
			m.toast(err.Error())
			return nil
		}
		m.modal = nil
		m.refreshBoard()
		m.rebuild()
		return nil
	case "backspace":
		field := fields[f.Focus]
		if rs := []rune(*field); len(rs) > 0 {
			*field = string(rs[:len(rs)-1])
		}
		return nil
	case "ctrl+u":
		*fields[f.Focus] = ""
		return nil
	case "space", " ":
		*fields[f.Focus] += " "
		return nil
	}
	if msg.Type == tea.KeyRunes {
		*fields[f.Focus] += string(msg.Runes)
	}
	return nil
}

func (m *Model) saveTaskForm() error {
	f := m.modal.taskForm
	complexity, err := strconv.Atoi(strings.TrimSpace(f.Complexity))
	if err != nil || complexity < 1 || complexity > 5 {
		return fmt.Errorf("complexity must be 1..5")
	}
	priority, err := strconv.Atoi(strings.TrimSpace(f.Priority))
	if err != nil || priority < 1 || priority > 5 {
		return fmt.Errorf("priority must be 1..5")
	}
	labels := splitCSV(f.Labels)
	in := tracker.CreateInput{
		Title: f.Title, Description: f.Description, Context: f.Context,
		Complexity: complexity, Priority: priority, Component: f.Component,
		Labels: labels, Epic: f.Epic, CreatedBy: "user", OriginSessionID: m.app.ActiveSessionID(), Status: tracker.StatusBacklog,
	}
	if f.TaskID == "" {
		task, err := m.app.Tracker().Create(context.Background(), in, "user")
		if err != nil {
			return err
		}
		m.toast("created " + task.ID)
		return nil
	}
	task, err := m.app.Tracker().Get(context.Background(), f.TaskID)
	if err != nil {
		return err
	}
	in.Status = task.Status
	in.Assignee = task.Assignee
	in.DependsOn = task.DependsOn
	in.WriteScope = task.WriteScope
	in.Acceptance = task.Acceptance
	if _, err := m.app.Tracker().Update(context.Background(), f.TaskID, in, "user"); err != nil {
		return err
	}
	m.toast("updated " + f.TaskID)
	return nil
}

func (m *Model) openTaskDetail(t *tracker.Task) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", t.ID, t.Title)
	fmt.Fprintf(&b, "status %s · priority %d · complexity %d · component %s\n", t.Status, t.Priority, t.Complexity, orDash(t.Component))
	if len(t.Labels) > 0 {
		fmt.Fprintf(&b, "labels: %s\n", strings.Join(t.Labels, ", "))
	}
	if t.Epic != "" {
		fmt.Fprintf(&b, "epic: %s\n", t.Epic)
	}
	fmt.Fprintf(&b, "created by %s · attempts %d\n", t.CreatedBy, t.Attempts)
	if t.Branch != "" {
		fmt.Fprintf(&b, "branch: %s\n", t.Branch)
	}
	if t.Commit != "" {
		fmt.Fprintf(&b, "commit: %s\n", t.Commit)
	}
	b.WriteString("\ndescription\n" + t.Description + "\n")
	if t.Context != "" {
		b.WriteString("\ncontext\n" + t.Context + "\n")
	}
	if len(t.Acceptance) > 0 {
		b.WriteString("\nacceptance\n- " + strings.Join(t.Acceptance, "\n- ") + "\n")
	}
	if len(t.DependsOn) > 0 {
		b.WriteString("\ndepends on: " + strings.Join(t.DependsOn, ", ") + "\n")
	}
	if len(t.WriteScope) > 0 {
		b.WriteString("write scope: " + strings.Join(t.WriteScope, ", ") + "\n")
	}
	comments, _ := m.app.Tracker().Comments(context.Background(), t.ID)
	if len(comments) > 0 {
		b.WriteString("\ncomments\n")
		for _, c := range comments {
			fmt.Fprintf(&b, "  %s: %s\n", c.Author, c.Body)
		}
	}
	events, _ := m.app.Tracker().Events(context.Background(), t.ID)
	if len(events) > 0 {
		b.WriteString("\nactivity\n")
		start := max(0, len(events)-12)
		for _, e := range events[start:] {
			fmt.Fprintf(&b, "  %s  %s %s\n", e.CreatedAt.Format("15:04"), e.Actor, e.Type)
		}
	}
	m.modal = &modalState{kind: modalTaskDetail, title: t.ID, body: b.String()}
}

func (m *Model) boardTransition(status tracker.TaskStatus) {
	t := m.selectedBoardTask()
	if t == nil {
		return
	}
	if _, err := m.app.Tracker().Transition(context.Background(), t.ID, status, "user"); err != nil {
		m.toast(err.Error())
		return
	}
	m.toast(t.ID + " → " + string(status))
	m.refreshBoard()
	m.rebuild()
}

func (m *Model) bumpPriority(delta int) {
	t := m.selectedBoardTask()
	if t == nil {
		return
	}
	p := t.Priority + delta
	if p > 5 {
		p = 1
	}
	if p < 1 {
		p = 5
	}
	in := tracker.CreateInput{
		Title: t.Title, Description: t.Description, Context: t.Context,
		Complexity: t.Complexity, Priority: p, Component: t.Component, Labels: t.Labels,
		Epic: t.Epic, DependsOn: t.DependsOn, WriteScope: t.WriteScope, Acceptance: t.Acceptance,
		Assignee: t.Assignee,
	}
	if _, err := m.app.Tracker().Update(context.Background(), t.ID, in, "user"); err != nil {
		m.toast(err.Error())
		return
	}
	m.toast(fmt.Sprintf("%s priority → %d", t.ID, p))
	m.refreshBoard()
	m.rebuild()
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
