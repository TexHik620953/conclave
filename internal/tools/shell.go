package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/llm"
)

type shellTool struct{}

func (shellTool) Name() string { return "shell" }
func (shellTool) Spec() llm.Tool {
	return llm.Tool{Name: "shell", Description: "Run a shell command in the workspace. Returns stdout, stderr and exit code.",
		Parameters: schema(map[string]any{
			"command": prop("string", "Shell command"),
			"timeout": prop("integer", "Timeout in seconds, default 120"),
		}, "command")}
}
func (shellTool) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	if env.Mode == "plan" {
		return "", fmt.Errorf("shell is disabled in plan mode")
	}
	if env.Permissions.Shell == "deny" {
		return "", fmt.Errorf("shell is disabled by permissions")
	}
	var in struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	if in.Timeout <= 0 {
		in.Timeout = 120
	}
	if in.Timeout > 600 {
		in.Timeout = 600
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
	defer cancel()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(cctx, shell, "-lc", in.Command)
	cmd.Dir = env.Cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var b strings.Builder
	fmt.Fprintf(&b, "exit: ")
	if err == nil {
		b.WriteString("0\n")
	} else if ee, ok := err.(*exec.ExitError); ok {
		fmt.Fprintf(&b, "%d\n", ee.ExitCode())
	} else if cctx.Err() == context.DeadlineExceeded {
		b.WriteString("timeout\n")
	} else {
		fmt.Fprintf(&b, "error: %v\n", err)
	}
	if stdout.Len() > 0 {
		b.WriteString("stdout:\n")
		b.WriteString(clip(stdout.String(), 80_000))
		b.WriteString("\n")
	}
	if stderr.Len() > 0 {
		b.WriteString("stderr:\n")
		b.WriteString(clip(stderr.String(), 40_000))
		b.WriteString("\n")
	}
	return b.String(), nil
}

type gitTool struct{}

func (gitTool) Name() string { return "git" }
func (gitTool) Spec() llm.Tool {
	return llm.Tool{Name: "git", Description: "Run a read-only git command such as status, diff, log or show.",
		Parameters: schema(map[string]any{"args": prop("string", "Arguments after `git`, e.g. status --short")}, "args")}
}
func (gitTool) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	var in struct {
		Args string `json:"args"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	allowed := []string{"status", "diff", "log", "show", "rev-parse", "branch", "ls-files"}
	first := strings.Fields(in.Args)
	if len(first) == 0 {
		return "", fmt.Errorf("git args are empty")
	}
	ok := false
	for _, a := range allowed {
		if first[0] == a {
			ok = true
		}
	}
	if !ok {
		return "", fmt.Errorf("git subcommand %q is not allowed", first[0])
	}
	cmd := exec.CommandContext(ctx, "git", first...)
	cmd.Dir = env.Cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git: %w", err)
	}
	return clip(string(out), 100_000), nil
}
