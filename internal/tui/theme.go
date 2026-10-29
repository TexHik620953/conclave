package tui

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name        string
	Bg          string
	Panel       string
	PanelAlt    string
	Border      string
	BorderFocus string
	Text        string
	Muted       string
	Accent      string
	Accent2     string
	Success     string
	Warning     string
	Error       string
	User        string
	Tool        string
	DiffAdd     string
	DiffDel     string
	DiffHunk    string
}

var themes = []Theme{
	{
		Name: "conclave-dark", Bg: "#0b0d16", Panel: "#121524", PanelAlt: "#181c2f",
		Border: "#2a3150", BorderFocus: "#7c6cf0", Text: "#e7e9f5", Muted: "#9aa0bd",
		Accent: "#a78bfa", Accent2: "#60a5fa", Success: "#4ade80", Warning: "#fbbf24",
		Error: "#fb7185", User: "#93c5fd", Tool: "#f9a8d4", DiffAdd: "#4ade80",
		DiffDel: "#fb7185", DiffHunk: "#60a5fa",
	},
	{
		Name: "tokyonight", Bg: "#1a1b26", Panel: "#1f2335", PanelAlt: "#24283b",
		Border: "#3b4261", BorderFocus: "#7aa2f7", Text: "#c0caf5", Muted: "#565f89",
		Accent: "#bb9af7", Accent2: "#7aa2f7", Success: "#9ece6a", Warning: "#e0af68",
		Error: "#f7768e", User: "#7aa2f7", Tool: "#ff9e64", DiffAdd: "#9ece6a",
		DiffDel: "#f7768e", DiffHunk: "#7aa2f7",
	},
	{
		Name: "catppuccin", Bg: "#1e1e2e", Panel: "#181825", PanelAlt: "#313244",
		Border: "#45475a", BorderFocus: "#cba6f7", Text: "#cdd6f4", Muted: "#7f849c",
		Accent: "#cba6f7", Accent2: "#89b4fa", Success: "#a6e3a1", Warning: "#f9e2af",
		Error: "#f38ba8", User: "#89b4fa", Tool: "#f5c2e7", DiffAdd: "#a6e3a1",
		DiffDel: "#f38ba8", DiffHunk: "#89b4fa",
	},
	{
		Name: "gruvbox", Bg: "#1d2021", Panel: "#282828", PanelAlt: "#32302f",
		Border: "#504945", BorderFocus: "#d79921", Text: "#ebdbb2", Muted: "#928374",
		Accent: "#fabd2f", Accent2: "#83a598", Success: "#b8bb26", Warning: "#fabd2f",
		Error: "#fb4934", User: "#83a598", Tool: "#d3869b", DiffAdd: "#b8bb26",
		DiffDel: "#fb4934", DiffHunk: "#83a598",
	},
	{
		Name: "system", Bg: "default", Panel: "default", PanelAlt: "default",
		Border: "#666666", BorderFocus: "#ffffff", Text: "default", Muted: "#888888",
		Accent: "default", Accent2: "default", Success: "green", Warning: "yellow",
		Error: "red", User: "cyan", Tool: "magenta", DiffAdd: "green",
		DiffDel: "red", DiffHunk: "blue",
	},
}

func themeByName(name string) Theme {
	for _, t := range themes {
		if t.Name == name {
			return t
		}
	}
	return themes[0]
}

func (t Theme) style(fg, bg string) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
	if bg != "" {
		s = s.Background(lipgloss.Color(bg))
	}
	return s
}

func (t Theme) base() lipgloss.Style   { return t.style(t.Text, t.Bg) }
func (t Theme) muted() lipgloss.Style  { return t.style(t.Muted, t.Bg) }
func (t Theme) accent() lipgloss.Style { return t.style(t.Accent, t.Bg) }
func (t Theme) ok() lipgloss.Style     { return t.style(t.Success, t.Bg) }
func (t Theme) warn() lipgloss.Style   { return t.style(t.Warning, t.Bg) }
func (t Theme) bad() lipgloss.Style    { return t.style(t.Error, t.Bg) }
