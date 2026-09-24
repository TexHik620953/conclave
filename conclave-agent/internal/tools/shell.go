package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type shellTool struct{ r *Registry }

func (t *shellTool) Name() string        { return "run_shell" }
func (t *shellTool) Description() string { return "Run a shell command in the workspace." }
func (t *shellTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"command":    map[string]any{"type": "string"},
		"timeout_ms": map[string]any{"type": "integer"},
	}, "command")
}

func (t *shellTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		return Result{Content: "command is required", IsError: true}, nil
	}
	timeout := t.r.timeout
	if in.TimeoutMS > 0 {
		timeout = time.Duration(in.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(runCtx, "sh", "-c", cmd)
	c.Dir = t.r.workspace
	out, err := c.CombinedOutput()
	result := truncate(string(out))
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Content: fmt.Sprintf("timed out after %s\n%s", timeout, result), IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("%s\nexit: %v", result, err), IsError: true}, nil
	}
	if result == "" {
		result = "(no output)"
	}
	return Result{Content: result}, nil
}

func truncate(s string) string {
	const max = 64 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}
