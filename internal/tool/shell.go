package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
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
	argv, err := t.env.buildArgv(in.Command)
	if err != nil {
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
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
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

// shellMeta lists characters that let a command chain, redirect, substitute or
// background another command. They are rejected outright when an allowlist is
// configured, so an allowed base command cannot smuggle in a second one.
const shellMeta = ";&|<>`$\n\r\\\"'"

// buildArgv validates a command against the shell policy and returns the argv
// to execute. Without an allowlist the command runs through `sh -c`; with an
// allowlist it is split into argv and executed directly (no shell), which
// makes the allowlist meaningful.
func (e *Env) buildArgv(command string) ([]string, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil, fmt.Errorf("empty command")
	}
	if strings.ContainsRune(trimmed, 0) {
		return nil, fmt.Errorf("command contains NUL byte")
	}
	lower := strings.ToLower(trimmed)
	for _, denied := range e.DeniedCommands {
		if denied != "" && strings.Contains(lower, strings.ToLower(denied)) {
			return nil, fmt.Errorf("command denied by policy: %q", denied)
		}
	}
	if len(e.AllowedCommands) == 0 {
		return []string{"sh", "-c", trimmed}, nil
	}
	if strings.ContainsAny(trimmed, shellMeta) {
		return nil, fmt.Errorf("shell metacharacters are not allowed when an allowlist is configured")
	}
	fields := strings.Fields(trimmed)
	base := fields[0]
	for _, allowed := range e.AllowedCommands {
		if base == allowed || filepath.Base(base) == allowed {
			return fields, nil
		}
	}
	return nil, fmt.Errorf("command %q is not in the allowlist", base)
}

// checkCommand reports whether a command would pass the shell policy.
func (e *Env) checkCommand(command string) error {
	_, err := e.buildArgv(command)
	return err
}
