package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateID(t *testing.T) {
	valid := []string{"feature-20260920-abc123", "run_1", "a.b", "UPPER"}
	for _, s := range valid {
		if err := ValidateID(s); err != nil {
			t.Errorf("%q should be valid: %v", s, err)
		}
	}
	invalid := []string{"", "../escape", "a/b", "..", "a..b", "a b", "a\x00b", "a;b"}
	for _, s := range invalid {
		if err := ValidateID(s); err == nil {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestSaveArtifactRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s, err := Open("", root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.SaveArtifact("../../etc", "x.md", "boom"); err == nil {
		t.Fatal("expected traversal run id to be rejected")
	}
	// A traversal-ish artifact name is neutralized, never escaping the run dir.
	if p, err := s.SaveArtifact("run1", "..", "boom"); err != nil {
		t.Fatalf("artifact name should be neutralized, not error: %v", err)
	} else if filepath.Dir(p) != filepath.Join(root, "runs", "run1") {
		t.Fatalf("neutralized artifact escaped: %q", p)
	}
	// A legit artifact must land inside the run directory.
	path, err := s.SaveArtifact("run1", "plan.md", "ok")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "runs", "run1")
	if filepath.Dir(path) != want {
		t.Fatalf("artifact path %q not inside %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("artifact not written: %v", err)
	}
}

func TestMarkStaleInterruptedKeepsFreshRuns(t *testing.T) {
	root := t.TempDir()
	s, err := Open("", root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateRun(Run{ID: "fresh"}); err != nil {
		t.Fatal(err)
	}
	// A fresh run with a recent heartbeat must survive.
	if err := s.MarkStaleInterrupted(); err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" {
		t.Fatalf("fresh run marked %q, want running", run.Status)
	}
}
