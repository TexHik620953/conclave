package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestListRunsHandlesNullColumns(t *testing.T) {
	dir := t.TempDir()
	st, err := Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Simulate a legacy/interrupted row with NULL error and finished_at.
	db, err := sql.Open("sqlite", filepath.Join(dir, "conclave.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runs (id, pipeline, status) VALUES ('legacy', 'p', 'running')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err = Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	runs, err := st.ListRuns(10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	if runs[0].Status != "interrupted" {
		t.Errorf("status = %q, want interrupted", runs[0].Status)
	}
	if runs[0].Error != "" {
		t.Errorf("error = %q, want empty", runs[0].Error)
	}
	if _, err := st.GetRun("legacy"); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
}

func TestCreateRunRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.CreateRun(Run{ID: "r1", Pipeline: "t", Task: "do", Workspace: dir}); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun("r1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" || run.Error != "" || !run.FinishedAt.IsZero() {
		t.Fatalf("unexpected run: %+v", run)
	}
	if err := st.FinishRun("r1", "completed", ""); err != nil {
		t.Fatal(err)
	}
	run, _ = st.GetRun("r1")
	if run.Status != "completed" || run.FinishedAt.IsZero() {
		t.Fatalf("unexpected run after finish: %+v", run)
	}
}
