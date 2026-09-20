package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type gitTool struct{ env *Env }

func (t *gitTool) Name() string { return "git" }
func (t *gitTool) Description() string {
	return "Run a git subcommand in the workspace (e.g. status, diff, log, add, commit)."
}
func (t *gitTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"args": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Arguments passed to git, e.g. [\"diff\", \"--stat\"].",
		},
	}, "args")
}

func (t *gitTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.Shell {
		return Result{Content: "shell permission denied", IsError: true}, nil
	}
	var in struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if len(in.Args) == 0 {
		return Result{Content: "args is required", IsError: true}, nil
	}
	cmd := exec.CommandContext(ctx, "git", in.Args...)
	if t.env.Workspace != "" {
		cmd.Dir = t.env.Workspace
	}
	out, err := cmd.CombinedOutput()
	result := truncate(t.env, string(out))
	if err != nil {
		return Result{Content: fmt.Sprintf("git %s\n%s\n%v", strings.Join(in.Args, " "), result, err), IsError: true}, nil
	}
	if result == "" {
		result = "(no output)"
	}
	return Result{Content: result}, nil
}
