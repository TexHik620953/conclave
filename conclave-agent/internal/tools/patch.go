package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type applyPatch struct{ r *Registry }

func (t *applyPatch) Name() string        { return "apply_patch" }
func (t *applyPatch) Description() string { return "Apply a unified diff patch to the workspace." }
func (t *applyPatch) Schema() json.RawMessage {
	return schema(map[string]any{
		"patch": map[string]any{"type": "string", "description": "Unified diff text."},
	}, "patch")
}

func (t *applyPatch) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Patch) == "" {
		return Result{Content: "patch is required", IsError: true}, nil
	}
	f, err := os.CreateTemp("", "conclave-*.patch")
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(in.Patch); err != nil {
		f.Close()
		return Result{Content: err.Error(), IsError: true}, nil
	}
	f.Close()

	runCtx, cancel := context.WithTimeout(ctx, t.r.timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", "apply", "--whitespace=nowarn", f.Name())
	cmd.Dir = t.r.workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Content: fmt.Sprintf("git apply failed: %v\n%s", err, truncate(string(out))), IsError: true}, nil
	}
	return Result{Content: "patch applied"}, nil
}
