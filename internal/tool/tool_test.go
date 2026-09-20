package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathSandbox(t *testing.T) {
	ws := t.TempDir()
	if _, err := resolvePath(ws, "../escape"); err == nil {
		t.Fatal("expected escape to be rejected")
	}
	got, err := resolvePath(ws, "sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(ws, "sub/file.txt") {
		t.Fatalf("resolvePath = %q", got)
	}
}

func TestWriteAndReadFile(t *testing.T) {
	ws := t.TempDir()
	env := &Env{Workspace: ws, FSRead: true, FSWrite: true}
	reg := NewRegistry(env)

	writeArgs, _ := json.Marshal(map[string]any{"path": "a/b.txt", "content": "hello\nworld"})
	res, err := reg.Execute(context.Background(), "write_file", writeArgs)
	if err != nil || res.IsError {
		t.Fatalf("write_file: %v %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(ws, "a/b.txt")); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	readArgs, _ := json.Marshal(map[string]any{"path": "a/b.txt"})
	res, err = reg.Execute(context.Background(), "read_file", readArgs)
	if err != nil || res.IsError {
		t.Fatalf("read_file: %v %+v", err, res)
	}
	if want := "1: hello\n2: world\n"; res.Content != want {
		t.Fatalf("read content = %q, want %q", res.Content, want)
	}
}

func TestPermissionsDeny(t *testing.T) {
	ws := t.TempDir()
	env := &Env{Workspace: ws, FSRead: true, FSWrite: false}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{"path": "x.txt", "content": "nope"})
	res, err := reg.Execute(context.Background(), "write_file", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected permission denial")
	}
}
