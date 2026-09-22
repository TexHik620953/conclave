package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/store"
)

func TestFollowupRerunsWithContext(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := config.Pipeline{
		Start: "a",
		Nodes: []config.Node{
			{ID: "a", Type: "transform", Template: "{{task}}"},
			{ID: "b", Type: "transform", Template: "{{task}}"},
		},
		Edges: []config.Edge{{From: "a", To: "b"}},
	}
	e := &Engine{
		Cfg: &config.Config{
			Pipelines: map[string]config.Pipeline{"t": p},
			Settings:  config.Settings{MaxParallel: 2, MaxIterations: 2, OnNoUser: "next"},
		},
		Store: st,
	}

	first, err := e.Run(context.Background(), RunOptions{Pipeline: "t", Task: "base", Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	if first.Outputs["b"] != "base" {
		t.Fatalf("first run b = %q", first.Outputs["b"])
	}

	// A follow-up must re-run every node with the previous outputs as context.
	second, err := e.Run(context.Background(), RunOptions{
		ResumeRunID: first.RunID,
		Followup:    "more detail please",
	})
	if err != nil {
		t.Fatalf("follow-up: %v", err)
	}
	got := second.Outputs["b"]
	if !strings.Contains(got, "base") || !strings.Contains(got, "## Follow-up") || !strings.Contains(got, "more detail please") {
		t.Fatalf("follow-up did not re-run with context: %q", got)
	}
	if second.Status != "completed" {
		t.Fatalf("status = %s", second.Status)
	}
}
