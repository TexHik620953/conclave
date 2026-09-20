package orchestrator

import (
	"regexp"
	"strings"
)

var templatePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

// renderTemplate substitutes {{...}} references from the run state.
func renderTemplate(tpl string, st *State) string {
	if tpl == "" {
		return ""
	}
	outputs, artifacts := st.Snapshot()
	return templatePattern.ReplaceAllStringFunc(tpl, func(match string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
		switch {
		case key == "task":
			return st.Task
		case strings.HasPrefix(key, "inputs."):
			return st.Inputs[strings.TrimPrefix(key, "inputs.")]
		case strings.HasPrefix(key, "outputs."):
			return outputs[strings.TrimPrefix(key, "outputs.")]
		case strings.HasPrefix(key, "artifacts."):
			return artifacts[strings.TrimPrefix(key, "artifacts.")]
		default:
			return outputs[key]
		}
	})
}
