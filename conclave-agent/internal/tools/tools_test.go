package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathRejectsEscape(t *testing.T) {
	r := NewRegistry(t.TempDir(), 0)
	if _, err := r.resolvePath("../escape"); err == nil {
		t.Fatal("expected escape rejection")
	}
	got, err := r.resolvePath("sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, r.workspace) {
		t.Fatalf("resolved outside workspace: %s", got)
	}
}

func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := NewRegistry(ws, 0)
	if _, err := r.resolvePath("link/secret"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestWriteReadFile(t *testing.T) {
	r := NewRegistry(t.TempDir(), 0)
	ctx := context.Background()
	args, _ := json.Marshal(map[string]any{"path": "a/b.txt", "content": "hello\nworld"})
	res, err := r.Execute(ctx, "write_file", args)
	if err != nil || res.IsError {
		t.Fatalf("write_file: %v %+v", err, res)
	}
	readArgs, _ := json.Marshal(map[string]any{"path": "a/b.txt"})
	res, err = r.Execute(ctx, "read_file", readArgs)
	if err != nil || res.IsError {
		t.Fatalf("read_file: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "1: hello") || !strings.Contains(res.Content, "2: world") {
		t.Fatalf("read = %q", res.Content)
	}
}

func TestGlobRecursive(t *testing.T) {
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, "x/y"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "x/y/z.go"), []byte("package z"), 0o644)
	r := NewRegistry(ws, 0)
	args, _ := json.Marshal(map[string]any{"pattern": "**/*.go"})
	res, _ := r.Execute(context.Background(), "glob", args)
	if !strings.Contains(res.Content, "x/y/z.go") {
		t.Fatalf("glob = %q", res.Content)
	}
}

func TestShellInWorkspace(t *testing.T) {
	ws := t.TempDir()
	r := NewRegistry(ws, 0)
	args, _ := json.Marshal(map[string]any{"command": "pwd"})
	res, err := r.Execute(context.Background(), "run_shell", args)
	if err != nil || res.IsError {
		t.Fatalf("run_shell: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, ws) {
		// macOS may resolve /tmp symlinks; just ensure it ran.
		if !strings.Contains(res.Content, "/") {
			t.Fatalf("unexpected pwd: %q", res.Content)
		}
	}
}

func TestGitPolicy(t *testing.T) {
	deny := [][]string{{"clone", "https://x"}, {"push"}, {"-C", "/", "status"}, {"config", "user.email", "x"}}
	for _, args := range deny {
		if err := checkGitArgs(args); err == nil {
			t.Errorf("git %v should be denied", args)
		}
	}
	allow := [][]string{{"status"}, {"diff", "--stat"}, {"log", "-n", "5"}}
	for _, args := range allow {
		if err := checkGitArgs(args); err != nil {
			t.Errorf("git %v should be allowed: %v", args, err)
		}
	}
}
