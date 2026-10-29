package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
)

func TestWriteEditReadAndSandbox(t *testing.T) {
	dir := t.TempDir()
	env := Env{Cwd: dir, Mode: domain.ModeBuild, Permissions: config.Permissions{OutsideCwd: "deny"}}
	r := Builtins()
	run := func(name, args string) string {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		out, err := tool.Run(context.Background(), env, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	run("write_file", `{"path":"a.txt","content":"one two one"}`)
	run("edit_file", `{"path":"a.txt","old_string":"two","new_string":"three"}`)
	got := run("read_file", `{"path":"a.txt"}`)
	if got == "" {
		t.Fatal("empty read")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "one three one" {
		t.Fatalf("content = %q", b)
	}
	tool, _ := r.Get("read_file")
	if _, err := tool.Run(context.Background(), env, json.RawMessage(`{"path":"../escape"}`)); err == nil {
		t.Fatal("sandbox escape was allowed")
	}
	env.Mode = domain.ModePlan
	tool, _ = r.Get("write_file")
	if _, err := tool.Run(context.Background(), env, json.RawMessage(`{"path":"x","content":"y"}`)); err == nil {
		t.Fatal("write in plan mode was allowed")
	}
}
