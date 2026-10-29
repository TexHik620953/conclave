package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/tracker"
)

func (m *Model) usableWidth() int {
	if m.width > 1 {
		return m.width - 1
	}
	return m.width
}

func (m *Model) layout() {
	if !m.ready {
		return
	}
	uw := m.usableWidth()
	m.input.SetWidth(max(20, uw-8))
	m.input.SetHeight(1)
	headerH, footerH, composerH := 2, 1, 5
	paletteH := m.paletteHeight()
	bodyH := m.height - headerH - footerH - composerH - paletteH
	if bodyH < 7 {
		bodyH = 7
	}
	sidebarW := 0
	if m.showSidebar && uw >= 100 {
		sidebarW = 32
	}
	mainW := uw - sidebarW
	if sidebarW > 0 {
		mainW--
	}
	vpH := bodyH - 4
	if vpH < 2 {
		vpH = 2
	}
	m.chatVP.Width = max(10, mainW-4)
	m.chatVP.Height = vpH
	m.dashVP.Width = max(10, mainW-4)
	m.dashVP.Height = vpH
	m.rebuild()
}

func (m *Model) bodyHeight() int {
	paletteH := m.paletteHeight()
	h := m.height - 2 - 1 - 5 - paletteH
	if h < 7 {
		h = 7
	}
	return h
}

func (m *Model) paletteHeight() int {
	if !m.paletteVisible() {
		return 0
	}
	n := len(m.paletteItems())
	h := n + 4
	limit := m.height - 2 - 1 - 5 - 7
	if limit > 12 {
		limit = 12
	}
	if limit < 5 {
		limit = 5
	}
	if h > limit {
		h = limit
	}
	if h < 5 {
		h = 5
	}
	return h
}

func (m *Model) rebuild() {
	if !m.ready {
		return
	}
	vp := m.activeViewport()
	atBottom := vp.AtBottom()
	width := vp.Width
	var content string
	if m.mode == domain.ModeConclave && m.tab == 1 {
		content = m.renderTasks(width)
	} else if m.mode == domain.ModeConclave && m.tab == 2 {
		content = m.renderAgents(width)
	} else {
		content = m.renderChat(width)
	}
	vp.SetContent(content)
	if len(m.chat) == 0 && !(m.mode == domain.ModeConclave && m.tab != 0) {
		vp.GotoTop()
	} else if atBottom || (m.busy && m.mode == domain.ModeConclave && m.tab == 0) {
		vp.GotoBottom()
	}
}

func (m *Model) View() string {
	if !m.ready {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render("starting conclave…")
	}
	parts := []string{m.renderHeader(), m.renderBody()}
	if p := m.renderPalette(); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, m.renderComposer(), m.renderFooter())
	uw := m.usableWidth()
	content := fitFrame(strings.Join(parts, "\n"), uw, m.height)
	return m.theme.base().
		Width(uw).
		Height(m.height).
		MaxWidth(uw).
		MaxHeight(m.height).
		Render(content)
}

func (m *Model) renderHeader() string {
	t := m.theme
	left := t.accent().Bold(true).Render("◆ CONCLAVE")
	mode := t.warn().Render(" PLAN ")
	switch m.mode {
	case domain.ModeBuild:
		if m.planOnly {
			mode = t.warn().Render(" BUILD · PLAN ")
		} else {
			mode = t.ok().Render(" BUILD ")
		}
	case domain.ModeConclave:
		mode = t.accent().Render(" MASTER ")
	}
	model := t.base().Bold(true).Render(shortModel(m.model))
	if m.effort != "" {
		model += t.muted().Render(" · " + m.effort)
	}
	left += "  " + mode + "  " + model
	right := t.muted().Render(truncate(m.state.Session.Title, 40))
	uw := m.usableWidth()
	gap := uw - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line1 := left + strings.Repeat(" ", gap) + right
	var line2 string
	if m.screen == 1 {
		p := m.app.Tracker().Project()
		st := m.app.Queue().Status()
		queue := "queue off"
		if st.Running {
			queue = fmt.Sprintf("queue on · %d active", st.Active)
		}
		line2 = t.accent().Render("BOARD ") + t.base().Render(p.Key+" · "+p.Name) + "   " + t.warn().Render(queue) + "   " + t.muted().Render(shortenPath(m.cwd, max(10, m.usableWidth()-50)))
	} else if m.mode == domain.ModeConclave {
		tabs := []string{"Chat", "Tasks", "Agents"}
		var parts []string
		for i, name := range tabs {
			if i == m.tab {
				parts = append(parts, t.accent().Bold(true).Render(" "+name+" "))
			} else {
				parts = append(parts, t.muted().Render(" "+name+" "))
			}
		}
		line2 = strings.Join(parts, " ") + "   " + t.muted().Render(shortenPath(m.cwd, max(10, uw-50)))
	} else {
		line2 = t.muted().Render("cwd " + shortenPath(m.cwd, uw-14))
	}
	return line1 + "\n" + line2
}

func (m *Model) renderBody() string {
	if m.modal != nil {
		return m.renderModal()
	}
	if m.screen == 1 {
		return m.renderBoard()
	}
	bodyH := m.bodyHeight()
	uw := m.usableWidth()
	sidebarW := 0
	if m.showSidebar && uw >= 100 {
		sidebarW = 32
	}
	mainW := uw - sidebarW
	if sidebarW > 0 {
		mainW--
	}
	title := "Chat"
	if m.mode == domain.ModeConclave {
		switch m.tab {
		case 1:
			title = "Tasks"
		case 2:
			title = "Agents"
		}
	}
	main := m.panel(title, m.activeViewport().View(), mainW, bodyH, true)
	if sidebarW == 0 {
		return main
	}
	side := m.panel("Usage", m.renderUsageSidebar(sidebarW-4, bodyH-4), sidebarW, bodyH, false)
	return lipgloss.JoinHorizontal(lipgloss.Top, main, " ", side)
}

func (m *Model) panel(title, body string, w, h int, focused bool) string {
	if w < 6 {
		w = 6
	}
	if h < 3 {
		h = 3
	}
	innerW, innerH := w-2, h-2
	border := m.theme.Border
	if focused {
		border = m.theme.BorderFocus
	}
	title = truncate(title, innerW)
	head := m.theme.accent().Background(lipgloss.Color(m.theme.Panel)).Bold(true).Render(title)
	if pad := innerW - ansi.StringWidth(head); pad > 0 {
		head += strings.Repeat(" ", pad)
	}
	head = paintBG(head, m.theme.Panel)
	sep := paintBG(m.theme.muted().Background(lipgloss.Color(m.theme.Panel)).Render(strings.Repeat("─", innerW)), m.theme.Panel)
	bodyLines := strings.Split(body, "\n")
	bodyH := innerH - 2
	if bodyH < 0 {
		bodyH = 0
	}
	if len(bodyLines) > bodyH {
		bodyLines = bodyLines[:bodyH]
	}
	for i, line := range bodyLines {
		line = ansi.Truncate(line, innerW, "")
		if pad := innerW - ansi.StringWidth(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		bodyLines[i] = paintBG(line, m.theme.Panel)
	}
	for len(bodyLines) < bodyH {
		bodyLines = append(bodyLines, paintBG(strings.Repeat(" ", innerW), m.theme.Panel))
	}
	content := head + "\n" + sep + "\n" + strings.Join(bodyLines, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(border)).
		Background(lipgloss.Color(m.theme.Panel)).
		Width(innerW).
		Height(innerH).
		Render(content)
}

func (m *Model) renderChat(width int) string {
	if len(m.chat) == 0 {
		return m.renderWelcome(width)
	}
	var b strings.Builder
	for i := range m.chat {
		e := &m.chat[i]
		if i > 0 {
			b.WriteString("\n")
		}
		switch e.kind {
		case "user":
			block := lipgloss.NewStyle().
				BorderLeft(true).
				BorderForeground(lipgloss.Color(m.theme.User)).
				Background(lipgloss.Color(m.theme.PanelAlt)).
				Padding(0, 1).
				Width(max(10, width-5)).
				Render(m.theme.style(m.theme.Text, m.theme.PanelAlt).Render(e.content))
			b.WriteString(block + "\n")
		case "assistant":
			var body string
			if e.streaming {
				body = m.theme.base().Background(lipgloss.Color(m.theme.Panel)).Render(e.content)
				if m.busy {
					body += m.theme.muted().Background(lipgloss.Color(m.theme.Panel)).Render(" ▍")
				}
			} else {
				if e.rendered == "" || e.renderedWidth != width-8 {
					e.rendered = renderMarkdown(e.content, max(20, width-8))
					e.renderedWidth = width - 8
				}
				body = e.rendered
			}
			block := lipgloss.NewStyle().
				BorderLeft(true).
				BorderForeground(lipgloss.Color(m.theme.Accent)).
				Padding(0, 1).
				Width(max(10, width-4)).
				Render(body)
			b.WriteString(block + "\n")
		case "tool":
			b.WriteString(m.renderToolCard(*e, width))
		case "system":
			b.WriteString(m.theme.style(m.theme.Muted, m.theme.PanelAlt).Render("  "+e.content+"  ") + "\n")
		case "error":
			b.WriteString(m.theme.bad().Background(lipgloss.Color(m.theme.Panel)).Render("Error: "+e.content) + "\n")
		}
	}
	if m.phase != "" {
		b.WriteString("\n" + m.theme.accent().Background(lipgloss.Color(m.theme.Panel)).Render(m.spin.View()+" "+m.phase) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderToolCard(e chatEntry, width int) string {
	t := m.theme
	status := "done"
	if e.failed {
		status = "failed"
	} else if e.streaming {
		status = "running"
	}
	title := "⚙ " + e.title + " · " + status
	color := t.Tool
	if e.failed {
		color = t.Error
	}
	head := t.style(color, t.PanelAlt).Bold(true).Render(title)
	body := e.content
	if !m.showToolDetail {
		body = firstLines(body, 3)
	} else {
		body = truncate(body, 12000)
	}
	body = renderToolOutput(body, t)
	if body == "" {
		body = t.muted().Render("no output yet")
	}
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Border)).
		Background(lipgloss.Color(t.PanelAlt)).
		Width(max(10, width-4)).
		Render(head + "\n" + body)
	return card + "\n"
}

func (m *Model) renderWelcome(width int) string {
	t := m.theme
	var b strings.Builder
	if width >= 70 && m.chatVP.Height >= 14 {
		logo := []string{
			" ██████╗ ██████╗ ███╗   ██╗ ██████╗██╗      █████╗ ██╗   ██╗███████╗",
			"██╔════╝██╔═══██╗████╗  ██║██╔════╝██║     ██╔══██╗██║   ██║██╔════╝",
			"██║     ██║   ██║██╔██╗ ██║██║     ██║     ███████║██║   ██║█████╗  ",
			"██║     ██║   ██║██║╚██╗██║██║     ██║     ██╔══██║╚██╗ ██╔╝██╔══╝  ",
			"╚██████╗╚██████╔╝██║ ╚████║╚██████╗███████╗██║  ██║ ╚████╔╝ ███████╗",
			" ╚═════╝ ╚═════╝ ╚═╝  ╚═══╝ ╚═════╝╚══════╝╚═╝  ╚═╝  ╚═══╝  ╚══════╝",
		}
		for _, line := range logo {
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Render(line) + "\n")
		}
	} else {
		b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render("C O N C L A V E") + "\n")
	}
	b.WriteString("\n")
	b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Bold(true).Render("Multi-agent coding in your terminal") + "\n")
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(shortenPath(m.cwd, max(10, width-2))) + "\n\n")
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("Type a request, or use:") + "\n")
	rows := [][2]string{
		{"/", "commands and suggestions"},
		{"ctrl+p", "command palette"},
		{"@", "file references (via the agent)"},
		{"tab", "switch Build / Plan / Conclave views"},
		{"ctrl+u", "tokens and context by model"},
		{"?", "help and key bindings"},
	}
	for _, r := range rows {
		b.WriteString("  " + t.accent().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("%-9s", r[0])) + t.muted().Background(lipgloss.Color(t.Panel)).Render(r[1]) + "\n")
	}
	if m.mode == domain.ModeConclave {
		b.WriteString("\n" + t.warn().Background(lipgloss.Color(t.Panel)).Render("Master planning · /approve to dispatch workers") + "\n")
	}
	return b.String()
}

func (m *Model) renderUsageSidebar(width, height int) string {
	t := m.theme
	var b strings.Builder
	rec := m.activeUsage()
	contextMax := rec.ContextWindow
	if contextMax == 0 {
		contextMax = m.state.ContextLimit
	}
	name := rec.Model
	if name == "" {
		name = shortModel(m.model)
	}
	provider := rec.Provider
	if provider == "" && m.state.Model.Provider != "" {
		provider = m.state.Model.Provider
	}
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("ACTIVE MODEL") + "\n")
	b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(name, width)) + "\n")
	if provider != "" {
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(truncate(provider, width)) + "\n")
	}
	if rec.Requests > 0 || contextMax > 0 {
		b.WriteString("\n")
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("context") + "\n")
		b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("%s / %s", tokens(rec.LastContext), tokens(contextMax))) + "\n")
		b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Render(contextBar(rec.LastContext, contextMax, max(8, width-2))) + "\n")
		b.WriteString("\n")
		b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render("in  "+tokens(rec.InputTokens)) + "\n")
		b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render("out "+tokens(rec.OutputTokens)) + "\n")
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("%d requests", rec.Requests)) + "\n")
	} else {
		b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("No requests yet.") + "\n")
	}
	if m.mode == domain.ModeConclave && m.run != nil && m.run.Run != nil {
		r := m.run.Run
		b.WriteString("\n")
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("RUN") + "\n")
		b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(strings.ToUpper(string(r.Phase))) + "\n")
		done, total := taskCounts(r.Tasks)
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("tasks %d/%d · agents %d", done, total, len(m.agents))) + "\n")
		if m.run.PendingPlan != nil {
			b.WriteString(t.warn().Background(lipgloss.Color(t.Panel)).Render("plan awaiting approval") + "\n")
		}
	}
	models := m.usage.Models()
	if len(models) > 0 {
		sort.Slice(models, func(i, j int) bool {
			if models[i].Provider != models[j].Provider {
				return models[i].Provider < models[j].Provider
			}
			return models[i].Model < models[j].Model
		})
		b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("MODELS") + "\n")
		maxRows := (height - 20) / 2
		if maxRows < 2 {
			maxRows = 2
		}
		for i, r := range models {
			if i >= maxRows {
				break
			}
			name := shortModel(r.Model)
			selected := modelKey(r.Provider, r.Model) == m.usageKey
			line := fmt.Sprintf("%-17s %5s", truncate(name, 17), pctText(r.LastContext, r.ContextWindow))
			if selected {
				b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
			} else {
				b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderTasks(width int) string {
	t := m.theme
	if m.run == nil || m.run.Run == nil {
		return t.muted().Background(lipgloss.Color(t.Panel)).Render("No Conclave run yet. Use the Chat tab to plan with the master.")
	}
	r := m.run.Run
	var b strings.Builder
	done, total := taskCounts(r.Tasks)
	b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(strings.ToUpper(string(r.Phase))))
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("  · pool %d/%d · tasks %d/%d · agents %d", r.Iteration+1, r.MaxIterations, done, total, len(m.agents))))
	b.WriteString("\n")
	if r.Goal != "" {
		b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(r.Goal, width)) + "\n")
	}
	if r.Error != "" {
		b.WriteString(t.bad().Background(lipgloss.Color(t.Panel)).Render(truncate(r.Error, width)) + "\n")
	}
	if m.run.PendingPlan != nil {
		p := m.run.PendingPlan
		b.WriteString("\n" + t.warn().Background(lipgloss.Color(t.Panel)).Bold(true).Render("PLAN AWAITING APPROVAL") + "\n")
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("Review the tasks, then run /approve.") + "\n")
		for _, task := range p.Tasks {
			b.WriteString("  " + m.taskIcon(task.Status) + " " + t.base().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("%-8s %-10s %s", task.ID, task.WorkerID, truncate(task.Title, width-30))) + "\n")
			if len(task.WriteScope) > 0 || len(task.DependsOn) > 0 {
				b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("           scope: "+truncate(strings.Join(task.WriteScope, ", "), width-24)+"  deps: "+strings.Join(task.DependsOn, ", ")) + "\n")
			}
		}
	}
	b.WriteString("\n")
	if len(r.Tasks) == 0 {
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("No tasks yet."))
		return b.String()
	}
	for i, task := range r.Tasks {
		line := fmt.Sprintf("%s %-9s %-10s %-8s %s", m.taskIcon(task.Status), task.ID, task.WorkerID, taskStatusText(task.Status), task.Title)
		if i == m.selTask {
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(line, width)) + "\n")
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
		}
		if task.Error != "" {
			b.WriteString(t.bad().Background(lipgloss.Color(t.Panel)).Render("    "+truncate(task.Error, width-6)) + "\n")
		}
	}
	if m.detail && m.selTask < len(r.Tasks) {
		task := r.Tasks[m.selTask]
		b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render(strings.Repeat("─", min(width, 60))) + "\n")
		b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(task.ID+" · "+task.Title) + "\n")
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("status %s · worker %s · attempts %d", task.Status, task.WorkerID, task.Attempts)) + "\n")
		if task.Branch != "" {
			b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("branch "+truncate(task.Branch, width-10)) + "\n")
		}
		if task.Commit != "" {
			b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("commit "+short(task.Commit, 12)) + "\n")
		}
		b.WriteString("\n" + t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(task.Spec, width)) + "\n")
		if len(task.Acceptance) > 0 {
			b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("acceptance") + "\n")
			for _, a := range task.Acceptance {
				b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render("  - "+truncate(a, width-4)) + "\n")
			}
		}
		if task.Summary != "" {
			b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("summary") + "\n")
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(task.Summary, 3000)) + "\n")
		}
		if task.Diff != "" {
			b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("diff") + "\n")
			b.WriteString(renderToolOutput(truncate(task.Diff, 16000), t) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderAgents(width int) string {
	t := m.theme
	list := m.sortedAgents()
	var b strings.Builder
	b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render("WORKER LANES"))
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("  · %d agents", len(list))) + "\n\n")
	if len(list) == 0 {
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("No worker agents yet. Approve a plan in Tasks to dispatch them."))
		return b.String()
	}
	for i, a := range list {
		line := fmt.Sprintf("%-9s %-9s %-14s %-7s %s", shortID(a.ID), a.Status, shortModel(a.Model), elapsed(a.Started), a.TaskID)
		if i == m.selAgent {
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(line, width)) + "\n")
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
		}
		if a.Last != "" {
			b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("    "+truncate(a.Last, width-6)) + "\n")
		}
	}
	if len(list) > 0 && m.selAgent < len(list) {
		a := list[m.selAgent]
		b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render(strings.Repeat("─", min(width, 60))) + "\n")
		b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(a.ID) + "\n")
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("%s · %s · task %s", shortModel(a.Model), a.Status, a.TaskID)) + "\n\n")
		start := 0
		if len(a.Events) > 18 {
			start = len(a.Events) - 18
		}
		for _, e := range a.Events[start:] {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(e, width)) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderPalette() string {
	if !m.paletteVisible() {
		return ""
	}
	items := m.paletteItems()
	if m.paletteSel >= len(items) {
		m.paletteSel = 0
	}
	title := "Commands"
	if len(items) > 0 && items[0].IsArg {
		title = "Arguments"
	}
	var b strings.Builder
	if len(items) == 0 {
		b.WriteString(m.theme.muted().Background(lipgloss.Color(m.theme.Panel)).Render("No matches. Keep typing or press esc."))
	}
	maxItems := m.paletteHeight() - 4
	start := 0
	if m.paletteSel >= maxItems {
		start = m.paletteSel - maxItems + 1
	}
	for i := start; i < len(items) && i < start+maxItems; i++ {
		it := items[i]
		label := it.Label
		if !it.IsArg {
			label = fmt.Sprintf("%-14s", it.Label)
		}
		line := label
		if it.Description != "" {
			line += "  " + it.Description
		}
		if i == m.paletteSel {
			b.WriteString(m.theme.accent().Background(lipgloss.Color(m.theme.Panel)).Bold(true).Render(truncate(line, m.usableWidth()-6)) + "\n")
		} else {
			b.WriteString(m.theme.base().Background(lipgloss.Color(m.theme.Panel)).Render(truncate(line, m.usableWidth()-6)) + "\n")
		}
	}
	return m.panel(title, strings.TrimRight(b.String(), "\n"), m.usableWidth(), m.paletteHeight(), true)
}

func (m *Model) renderComposer() string {
	body := m.input.View()
	return m.panel("Prompt", body, m.usableWidth(), 5, true)
}

func (m *Model) renderFooter() string {
	t := m.theme
	hint := "/ commands · ctrl+p palette · ctrl+t board · ctrl+b sidebar · ctrl+u usage · ? help"
	uw := m.usableWidth()
	if uw < 118 {
		hint = "/ commands · ctrl+p · ctrl+t · ctrl+u · ? help"
	}
	left := t.muted().Background(lipgloss.Color(t.Bg)).Render(hint)
	statusText := m.status
	if m.errText != "" {
		statusText = m.errText
	} else if m.toastText != "" {
		statusText = m.toastText
	}
	if m.phase != "" {
		statusText = m.spin.View() + " " + m.phase
	} else if m.busy {
		statusText = m.spin.View() + " " + statusText
	}
	color := t.Muted
	if m.errText != "" {
		color = t.Error
	}
	right := t.style(color, t.Bg).Render(truncate(statusText, 50))
	gap := uw - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return t.muted().Background(lipgloss.Color(t.Bg)).Render(truncate(hint+"  ·  "+statusText, uw))
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) activeUsage() domain.UsageRecord {
	key := m.usageKey
	if rec, ok := m.usage.ByModel[key]; ok {
		return rec
	}
	if m.state.Model.Provider != "" {
		k := modelKey(m.state.Model.Provider, m.state.Model.ID)
		if rec, ok := m.usage.ByModel[k]; ok {
			return rec
		}
	}
	maxCtx := m.state.ContextLimit
	if m.mode == domain.ModeConclave && m.run != nil && m.run.MasterContext > 0 {
		maxCtx = m.run.MasterContext
	}
	return domain.UsageRecord{ContextWindow: maxCtx}
}

func (m *Model) renderModal() string {
	md := m.modal
	bodyH := m.bodyHeight()
	uw := m.usableWidth()
	w := min(84, uw-6)
	h := min(24, bodyH-2)
	if w < 30 {
		w = max(20, uw-2)
	}
	if h < 6 {
		h = 6
	}
	var body string
	switch md.kind {
	case modalModels, modalThemes, modalProviders:
		body = m.renderPickerModal(md, w-4, h-5)
	case modalUsage:
		body = m.renderUsageModal(md, w-4, h-5)
	case modalAddProvider:
		body = m.renderProviderForm(md, w-4, h-5)
	case modalTaskForm:
		body = m.renderTaskForm(md, w-4, h-5)
	case modalTaskDetail:
		body = clipLines(md.body, h-5)
	case modalLoading:
		body = m.theme.accent().Background(lipgloss.Color(m.theme.Panel)).Render(md.body) + "\n\n" +
			m.theme.muted().Background(lipgloss.Color(m.theme.Panel)).Render("esc cancel")
	default:
		body = clipLines(md.body, h-5)
	}
	panel := m.panel(md.title, body, w, h, true)
	return lipgloss.Place(uw, bodyH, lipgloss.Center, lipgloss.Center, panel)
}

func (m *Model) renderBoard() string {
	bodyH := m.bodyHeight()
	uw := m.usableWidth()
	if m.app.Tracker() == nil {
		return m.panel("Board", m.theme.muted().Background(lipgloss.Color(m.theme.Panel)).Render("Task tracker unavailable"), uw, bodyH, true)
	}
	p := m.app.Tracker().Project()
	if m.width < 100 {
		return m.renderBoardCompact(p, uw, bodyH)
	}
	const gaps = 4
	colW := (uw - gaps) / len(boardStatuses)
	if colW < 12 {
		return m.renderBoardCompact(p, uw, bodyH)
	}
	var cols []string
	for i, status := range boardStatuses {
		col := m.renderBoardColumn(status, colW, bodyH, i == m.boardCol)
		cols = append(cols, col)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols[0], " ", cols[1], " ", cols[2], " ", cols[3], " ", cols[4])
}

func (m *Model) renderBoardColumn(status tracker.TaskStatus, width, height int, focused bool) string {
	t := m.theme
	tasks := m.boardTasksByStatus(status)
	title := strings.ToUpper(string(status)) + fmt.Sprintf("  %d", len(tasks))
	var body strings.Builder
	if len(tasks) == 0 {
		body.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("—"))
	} else {
		selected := -1
		if status == boardStatuses[m.boardCol] {
			selected = m.boardSel
		}
		visible := max(1, (height-5)/3)
		start := 0
		if selected >= visible {
			start = selected - visible + 1
		}
		for i := start; i < len(tasks) && i < start+visible; i++ {
			task := tasks[i]
			line1 := fmt.Sprintf("%-8s P%d C%d", task.ID, task.Priority, task.Complexity)
			line2 := truncate(task.Title, width-4)
			line3 := task.CreatedBy
			if task.Assignee != "" {
				line3 = task.Assignee
			} else if len(task.Labels) > 0 {
				line3 = strings.Join(task.Labels, ",")
			}
			block := line1 + "\n" + line2 + "\n" + line3
			if i == selected {
				body.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(block) + "\n")
			} else {
				body.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(block) + "\n")
			}
			body.WriteString("\n")
		}
		if len(tasks) > start+visible {
			body.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("… %d more", len(tasks)-(start+visible))) + "\n")
		}
	}
	return m.panel(title, strings.TrimRight(body.String(), "\n"), width, height, focused)
}

func (m *Model) renderBoardCompact(p tracker.Project, width, height int) string {
	t := m.theme
	status := boardStatuses[m.boardCol]
	tasks := m.boardTasksByStatus(status)
	var b strings.Builder
	b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(p.Key+" · "+p.Name) + "\n")
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("◀ %s (%d) ▶   arrows switch columns · n new · enter details", strings.ToUpper(string(status)), len(tasks))) + "\n\n")
	if len(tasks) == 0 {
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("No tasks in this column."))
	}
	for i, task := range tasks {
		line := fmt.Sprintf("%-9s P%d C%d  %s", task.ID, task.Priority, task.Complexity, truncate(task.Title, width-28))
		if i == m.boardSel {
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(line) + "\n")
			if task.Description != "" {
				b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("   "+truncate(task.Description, width-6)) + "\n")
			}
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(line) + "\n")
		}
	}
	return m.panel("Board", strings.TrimRight(b.String(), "\n"), width, height, true)
}

func (m *Model) boardTasksByStatus(status tracker.TaskStatus) []tracker.Task {
	var out []tracker.Task
	for _, t := range m.boardTasks {
		if t.Status == status {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) renderTaskForm(md *modalState, width, height int) string {
	t := m.theme
	f := md.taskForm
	fields := []struct{ label, value string }{
		{"Title", f.Title},
		{"Description", f.Description},
		{"Context", f.Context},
		{"Complexity", f.Complexity},
		{"Priority", f.Priority},
		{"Component", f.Component},
		{"Labels", f.Labels},
		{"Epic", f.Epic},
	}
	var b strings.Builder
	for i, field := range fields {
		value := truncate(field.value, width-14)
		line := fmt.Sprintf("%-12s %s", field.label, value)
		if i == f.Focus {
			line += "▏"
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(line, width)) + "\n")
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
		}
	}
	b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("tab next · enter continue/save · ctrl+u clear · esc cancel"))
	return b.String()
}

func (m *Model) renderProviderForm(md *modalState, width, height int) string {
	t := m.theme
	f := md.form
	fields := []struct {
		label string
		value string
		key   bool
	}{
		{"Name", f.Name, false},
		{"Base URL", f.URL, false},
		{"API key", f.Key, true},
	}
	var b strings.Builder
	for i, field := range fields {
		value := field.value
		if field.key {
			n := len([]rune(value))
			if n > 0 {
				value = strings.Repeat("•", n)
			}
		}
		line := fmt.Sprintf("%-9s %s", field.label, value)
		if i == f.Focus {
			line += "▏"
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(line, width)) + "\n")
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("tab next · enter continue/save · ctrl+u clear · esc cancel"))
	return b.String()
}

func (m *Model) renderPickerModal(md *modalState, width, height int) string {
	t := m.theme
	if len(md.items) == 0 {
		return t.muted().Background(lipgloss.Color(t.Panel)).Render("Nothing to choose.")
	}
	if md.sel >= len(md.items) {
		md.sel = len(md.items) - 1
	}
	var b strings.Builder
	start := 0
	if md.sel >= height {
		start = md.sel - height + 1
	}
	for i := start; i < len(md.items) && i < start+height; i++ {
		it := md.items[i]
		line := fmt.Sprintf("%-16s", it.Label)
		if it.Description != "" {
			line += "  " + t.muted().Render(it.Description)
		}
		if i == md.sel {
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(line, width)) + "\n")
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
		}
	}
	b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("↑↓ select · enter choose · esc close"))
	return b.String()
}

func (m *Model) renderUsageModal(md *modalState, width, height int) string {
	t := m.theme
	var items []domain.UsageRecord
	if md.usageTab == 1 {
		items = m.usage.Agents()
	} else {
		items = m.usage.Models()
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Provider != items[j].Provider {
			return items[i].Provider < items[j].Provider
		}
		return items[i].Model < items[j].Model
	})
	var b strings.Builder
	if m.mode == domain.ModeConclave {
		tabs := []string{"Models", "Agents"}
		for i, name := range tabs {
			if i == md.usageTab {
				b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render("[" + name + "] "))
			} else {
				b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(" " + name + "  "))
			}
		}
		b.WriteString("\n\n")
	}
	if len(items) == 0 {
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("No usage recorded yet."))
		return b.String()
	}
	if md.sel >= len(items) {
		md.sel = len(items) - 1
	}
	header := fmt.Sprintf("%-11s %-22s %12s %9s %9s %5s", "PROVIDER", "MODEL", "CONTEXT", "INPUT", "OUTPUT", "REQ")
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render(truncate(header, width)) + "\n")
	for i, r := range items {
		if i >= height-5 {
			break
		}
		name := shortModel(r.Model)
		ctx := pctText(r.LastContext, r.ContextWindow)
		line := fmt.Sprintf("%-11s %-22s %12s %9s %9s %5d", truncate(r.Provider, 11), truncate(name, 22), ctx, tokens(r.InputTokens), tokens(r.OutputTokens), r.Requests)
		if i == md.sel {
			b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Bold(true).Render(truncate(line, width)) + "\n")
		} else {
			b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(truncate(line, width)) + "\n")
		}
	}
	sel := items[md.sel]
	b.WriteString("\n")
	b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("selected") + "\n")
	b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("%s/%s · ctx %s / %s", sel.Provider, sel.Model, tokens(sel.LastContext), tokens(sel.ContextWindow))) + "\n")
	b.WriteString(t.accent().Background(lipgloss.Color(t.Panel)).Render(contextBar(sel.LastContext, sel.ContextWindow, max(10, min(width-4, 60)))) + "\n")
	b.WriteString(t.base().Background(lipgloss.Color(t.Panel)).Render(fmt.Sprintf("input %s · output %s · cached %s · reasoning %s", tokens(sel.InputTokens), tokens(sel.OutputTokens), tokens(sel.CachedTokens), tokens(sel.ReasoningTokens))) + "\n")
	if md.usageTab == 1 {
		b.WriteString(t.muted().Background(lipgloss.Color(t.Panel)).Render("agent "+sel.AgentID+" · task "+sel.TaskID) + "\n")
	}
	b.WriteString("\n" + t.muted().Background(lipgloss.Color(t.Panel)).Render("↑↓ select · tab models/agents · enter close · esc close"))
	return b.String()
}

func (m *Model) taskIcon(s domain.TaskStatus) string {
	t := m.theme
	switch s {
	case domain.TaskDone:
		return t.ok().Background(lipgloss.Color(t.Panel)).Render("✔")
	case domain.TaskFailed:
		return t.bad().Background(lipgloss.Color(t.Panel)).Render("✖")
	case domain.TaskRunning:
		return t.accent().Background(lipgloss.Color(t.Panel)).Render("●")
	case domain.TaskQueued:
		return t.warn().Background(lipgloss.Color(t.Panel)).Render("◌")
	case domain.TaskBlocked:
		return t.muted().Background(lipgloss.Color(t.Panel)).Render("⊘")
	case domain.TaskReview:
		return t.style(t.Accent2, t.Panel).Render("◎")
	case domain.TaskCanceled:
		return t.muted().Background(lipgloss.Color(t.Panel)).Render("–")
	default:
		return t.muted().Background(lipgloss.Color(t.Panel)).Render("·")
	}
}

func taskStatusText(s domain.TaskStatus) string {
	switch s {
	case domain.TaskDone:
		return "done"
	case domain.TaskFailed:
		return "failed"
	case domain.TaskRunning:
		return "running"
	case domain.TaskQueued:
		return "queued"
	case domain.TaskBlocked:
		return "blocked"
	case domain.TaskReview:
		return "review"
	case domain.TaskCanceled:
		return "cancelled"
	default:
		return "pending"
	}
}

func renderMarkdown(s string, width int) string {
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(max(20, width)))
	if err != nil {
		return s
	}
	out, err := r.Render(s)
	if err != nil {
		return s
	}
	return strings.TrimRight(out, "\n")
}

func renderToolOutput(s string, t Theme) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		var styled string
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			styled = t.style(t.DiffHunk, t.PanelAlt).Render(line)
		case strings.HasPrefix(line, "+"):
			styled = t.style(t.DiffAdd, t.PanelAlt).Render(line)
		case strings.HasPrefix(line, "-"):
			styled = t.style(t.DiffDel, t.PanelAlt).Render(line)
		case strings.HasPrefix(line, "@@"):
			styled = t.style(t.DiffHunk, t.PanelAlt).Render(line)
		case strings.HasPrefix(line, "❯") || strings.HasPrefix(line, ">"):
			styled = t.accent().Background(lipgloss.Color(t.PanelAlt)).Render(line)
		default:
			styled = t.base().Background(lipgloss.Color(t.PanelAlt)).Render(line)
		}
		b.WriteString(styled + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func refKey(ref string) string {
	provider, model, ok := strings.Cut(ref, "/")
	if !ok {
		return ref
	}
	return modelKey(provider, model)
}

func modelKey(provider, model string) string {
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

func tokens(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

func pctText(cur, max int) string {
	if max <= 0 {
		return tokens(cur)
	}
	return fmt.Sprintf("%s/%s", tokens(cur), tokens(max))
}

func contextBar(cur, max, width int) string {
	if width < 4 {
		width = 4
	}
	if max <= 0 {
		return strings.Repeat("░", width)
	}
	filled := int(float64(cur) / float64(max) * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func taskCounts(tasks []domain.Task) (done, total int) {
	for _, t := range tasks {
		if t.Status == domain.TaskDone {
			done++
		}
	}
	return done, len(tasks)
}

func shortModel(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func shortID(s string) string {
	if len(s) <= 9 {
		return s
	}
	return s[:9]
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func elapsed(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}

func truncate(s string, n int) string {
	if n < 1 {
		n = 1
	}
	return ansi.Truncate(s, n, "…")
}

// fitFrame makes every frame exactly width x height. This prevents stale
// glyphs and colour bleed when panels shrink, the palette opens or a modal
// closes. Width is measured after ANSI and wide-rune handling.
func paintBG(line, hex string) string {
	if hex == "" || hex == "default" || !strings.HasPrefix(hex, "#") || len(hex) != 7 {
		return line
	}
	var r, g, b int
	_, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	if err != nil {
		return line
	}
	seq := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+seq)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+seq)
	return seq + line + "\x1b[0m"
}

func fitFrame(content string, width, height int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		line = ansi.Truncate(line, width, "")
		pad := width - ansi.StringWidth(line)
		if pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lines[i] = line
	}
	blank := strings.Repeat(" ", width)
	for len(lines) < height {
		lines = append(lines, blank)
	}
	return strings.Join(lines, "\n")
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n…"
}

func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n…"
}

func shortenPath(p string, maxLen int) string {
	if len(p) <= maxLen {
		return p
	}
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}

func appendCapped(list []string, v string, cap int) []string {
	list = append(list, v)
	if len(list) > cap {
		list = list[len(list)-cap:]
	}
	return list
}

func themeDescription(name string) string {
	switch name {
	case "conclave-dark":
		return "deep navy, violet accent"
	case "tokyonight":
		return "Tokyo Night"
	case "catppuccin":
		return "Catppuccin Macchiato"
	case "gruvbox":
		return "Gruvbox dark"
	default:
		return "terminal default"
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
