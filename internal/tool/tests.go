package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type runTests struct{ env *Env }

func (t *runTests) Name() string { return "run_tests" }
func (t *runTests) Description() string {
	return "Detect the project's test runner and run the test suite in the workspace."
}
func (t *runTests) Schema() json.RawMessage {
	return schema(map[string]any{
		"target": map[string]any{"type": "string", "description": "Optional package/path to test (Go only)."},
	})
}

func (t *runTests) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.Shell {
		return Result{Content: "shell permission denied", IsError: true}, nil
	}
	var in struct {
		Target string `json:"target"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, err
		}
	}
	name, cmdArgs, err := detectTestCommand(t.env.Workspace, in.Target)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	timeout := t.env.CommandTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, cmdArgs...)
	if t.env.Workspace != "" {
		cmd.Dir = t.env.Workspace
	}
	out, err := cmd.CombinedOutput()
	result := truncate(t.env, string(out))
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Content: fmt.Sprintf("tests timed out after %s\n%s", timeout, result), IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("%s\n%s", result, err), IsError: true}, nil
	}
	if result == "" {
		result = "(no output)"
	}
	return Result{Content: result}, nil
}

func detectTestCommand(workspace, target string) (string, []string, error) {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(workspace, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		if target != "" {
			return "go", []string{"test", target}, nil
		}
		return "go", []string{"test", "./..."}, nil
	case has("Cargo.toml"):
		return "cargo", []string{"test"}, nil
	case has("package.json"):
		return "npm", []string{"test", "--silent"}, nil
	case has("pytest.ini") || has("pyproject.toml") || has("setup.py") || has("tox.ini"):
		return "pytest", []string{"-q"}, nil
	}
	return "", nil, fmt.Errorf("could not detect a test runner in %s", workspace)
}
