package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type gitTool struct{ r *Registry }

func (t *gitTool) Name() string { return "git" }
func (t *gitTool) Description() string {
	return "Run a git subcommand in the workspace (e.g. status, diff, log)."
}
func (t *gitTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "args")
}

var gitDeniedOptions = map[string]bool{
	"-C": true, "--git-dir": true, "--work-tree": true, "-c": true,
	"--exec-path": true, "--namespace": true, "--upload-pack": true,
	"--receive-pack": true, "--exec": true, "--config-env": true, "--super-prefix": true,
}

var gitDeniedSubcommands = map[string]bool{
	"clone": true, "fetch": true, "pull": true, "push": true, "remote": true,
	"submodule": true, "ls-remote": true, "daemon": true, "filter-branch": true,
	"credential": true, "init": true, "worktree": true, "shell": true, "config": true,
}

func checkGitArgs(args []string) error {
	for _, a := range args {
		if strings.ContainsAny(a, "\n\r\x00") {
			return fmt.Errorf("invalid git argument")
		}
		name := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			name = a[:i]
		}
		if gitDeniedOptions[name] {
			return fmt.Errorf("git option %q is not allowed", name)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if gitDeniedSubcommands[a] {
			return fmt.Errorf("git subcommand %q is not allowed", a)
		}
		break
	}
	return nil
}

func (t *gitTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if len(in.Args) == 0 {
		return Result{Content: "args is required", IsError: true}, nil
	}
	if err := checkGitArgs(in.Args); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, t.r.timeout)
	defer cancel()
	c := exec.CommandContext(runCtx, "git", in.Args...)
	c.Dir = t.r.workspace
	out, err := c.CombinedOutput()
	result := truncate(string(out))
	if err != nil {
		return Result{Content: fmt.Sprintf("git %s\n%s\n%v", strings.Join(in.Args, " "), result, err), IsError: true}, nil
	}
	if result == "" {
		result = "(no output)"
	}
	return Result{Content: result}, nil
}
