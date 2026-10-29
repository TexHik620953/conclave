package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func TestResolveRepoGroupWorkspace(t *testing.T) {
	root := t.TempDir()
	initRepo(t, filepath.Join(root, "node"))
	initRepo(t, filepath.Join(root, "api"))

	repo, err := ResolveRepo(context.Background(), root, "node/internal/server.go")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(repo) != "node" {
		t.Fatalf("got %s, want node", repo)
	}

	repo, err = ResolveRepo(context.Background(), root, "", "Fix the API service", "api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(repo) != "api" {
		t.Fatalf("got %s, want api", repo)
	}
}

func TestResolveRepoSingleRepository(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	repo, err := ResolveRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if repo != root {
		t.Fatalf("got %s, want %s", repo, root)
	}
}
