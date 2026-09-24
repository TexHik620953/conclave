package playbook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinsLoad(t *testing.T) {
	r, err := LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ba", "general", "backend", "frontend", "security_review", "dba", "design", "architecture"} {
		if _, ok := r.Get(id); !ok {
			t.Errorf("builtin playbook %q not loaded", id)
		}
	}
}

func TestLoadDirAndVersioning(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("custom.yaml", "id: custom\nversion: 1\ntitle: v1\ncontrol: supervisor\nroles:\n  - {tier: senior}\n")
	write("custom_v2.yaml", "id: custom\nversion: 2\ntitle: v2\ncontrol: supervisor\nroles:\n  - {tier: senior}\n")

	r := NewRegistry()
	if err := r.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	pb, ok := r.Get("custom")
	if !ok || pb.Version != 2 || pb.Title != "v2" {
		t.Fatalf("expected latest version 2, got %+v ok=%v", pb, ok)
	}
	if len(r.List()) != 1 {
		t.Fatalf("list = %d, want 1", len(r.List()))
	}
}

func TestLoadDirEmpty(t *testing.T) {
	r := NewRegistry()
	if err := r.LoadDir(""); err != nil {
		t.Fatal(err)
	}
}
