package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatch(t *testing.T) {
	ws := t.TempDir()
	orig := "line1\nold\nline3\n"
	if err := os.WriteFile(filepath.Join(ws, "foo.txt"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := `--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,3 @@
 line1
-old
+new
 line3
`
	env := &Env{Workspace: ws, FSRead: true, FSWrite: true}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{"patch": diff})
	res, err := reg.Execute(context.Background(), "apply_patch", args)
	if err != nil || res.IsError {
		t.Fatalf("apply_patch: %v %+v", err, res)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "foo.txt"))
	if string(got) != "line1\nnew\nline3\n" {
		t.Fatalf("content = %q", string(got))
	}
}

func TestApplyPatchMismatch(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "foo.txt"), []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := `--- a/foo.txt
+++ b/foo.txt
@@ -1,1 +1,1 @@
-old
+new
`
	env := &Env{Workspace: ws, FSRead: true, FSWrite: true}
	reg := NewRegistry(env)
	args, _ := json.Marshal(map[string]any{"patch": diff})
	res, err := reg.Execute(context.Background(), "apply_patch", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected patch mismatch error")
	}
}

func TestDetectTestCommand(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	name, args, err := detectTestCommand(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "go" || len(args) != 2 || args[0] != "test" || args[1] != "./..." {
		t.Fatalf("detected %s %v", name, args)
	}
	if _, _, err := detectTestCommand(t.TempDir(), ""); err == nil {
		t.Fatal("expected detection error for empty dir")
	}
}
