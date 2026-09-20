package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type shellTool struct{ env *Env }

func (t *shellTool) Name() string { return "run_shell" }
func (t *shellTool) Description() string {
	return "Run a shell command inside the workspace and return combined stdout/stderr."
}
func (t *shellTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"command":    map[string]any{"type": "string"},
		"timeout_ms": map[string]any{"type": "integer", "description": "Optional timeout in milliseconds."},
	}, "command")
}

func (t *shellTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.Shell {
		return Result{Content: "shell permission denied", IsError: true}, nil
	}
	var in struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if err := t.env.checkCommand(in.Command); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	timeout := t.env.CommandTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if in.TimeoutMS > 0 {
		timeout = time.Duration(in.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-c", in.Command)
	if t.env.Workspace != "" {
		cmd.Dir = t.env.Workspace
	}
	out, err := cmd.CombinedOutput()
	result := truncate(t.env, string(out))
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Content: fmt.Sprintf("command timed out after %s\n%s", timeout, result), IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("%s\nexit: %v", result, err), IsError: true}, nil
	}
	if result == "" {
		result = "(no output)"
	}
	return Result{Content: result}, nil
}

func (e *Env) checkCommand(command string) error {
	trimmed := strings.TrimSpace(command)
	for _, denied := range e.DeniedCommands {
		if denied != "" && strings.Contains(trimmed, denied) {
			return fmt.Errorf("command denied by policy: %q", denied)
		}
	}
	if len(e.AllowedCommands) == 0 {
		return nil
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return fmt.Errorf("empty command")
	}
	base := fields[0]
	for _, allowed := range e.AllowedCommands {
		if base == allowed {
			return nil
		}
	}
	return fmt.Errorf("command %q is not in the allowlist", base)
}
