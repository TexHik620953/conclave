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

// gitDeniedSubcommands are subcommands that escape the workspace, touch the
// network or rewrite global state.
var gitDeniedSubcommands = map[string]bool{
	"clone": true, "fetch": true, "pull": true, "push": true, "remote": true,
	"submodule": true, "ls-remote": true, "daemon": true, "filter-branch": true,
	"credential": true, "credential-helper": true, "init": true, "worktree": true,
	"archive": true, "bundle": true, "send-email": true, "request-pull": true,
	"shell": true, "config": true,
}

// gitDeniedOptions redirect git at another directory or execute arbitrary code.
var gitDeniedOptions = map[string]bool{
	"-C": true, "--git-dir": true, "--work-tree": true, "-c": true,
	"--exec-path": true, "--namespace": true, "--upload-pack": true,
	"--receive-pack": true, "--exec": true, "--config-env": true,
	"--super-prefix": true,
}

// checkGitArgs rejects git invocations that could leave the workspace, reach
// the network or run arbitrary commands.
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
	// First non-option argument is the subcommand.
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
	if err := checkGitArgs(in.Args); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
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
