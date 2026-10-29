package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSnapshotIncludesUntrackedWithoutDirtyingIndex(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "a@b.c")
	run("config", "user.name", "a")
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")
	os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("two"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644)
	repo := Repo{Dir: dir}
	snap, err := repo.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "show", snap+":new.txt").Output()
	if err != nil || string(out) != "new" {
		t.Fatalf("snapshot missing untracked file: %v %q", err, out)
	}
	status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if string(status) == "" {
		t.Fatal("snapshot modified the real working tree state")
	}
}
