package orchestrator

import (
	"context"
	"testing"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/store"
)

func TestResumeSkipsCompletedNodes(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := config.Pipeline{
		Start: "start",
		Nodes: []config.Node{
			{ID: "start", Type: "transform", Template: "S-template"},
			{ID: "a", Type: "transform", Template: "A-template"},
			{ID: "b", Type: "transform", Template: "B"},
			{ID: "c", Type: "transform", Template: "C"},
		},
		Edges: []config.Edge{
			{From: "start", To: "a"},
			{From: "a", To: "b"},
			{From: "b", To: "c"},
		},
	}

	runID := "run-1"
	if err := st.CreateRun(store.Run{ID: runID, Pipeline: "t", Task: "task", Workspace: dir}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []store.NodeRecord{
		{RunID: runID, NodeID: "start", Status: "completed", Output: "S-stored"},
		{RunID: runID, NodeID: "a", Status: "completed", Output: "A-stored"},
	} {
		if err := st.SaveNode(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.FinishRun(runID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}

	e := &Engine{
		Cfg: &config.Config{
			Pipelines: map[string]config.Pipeline{"t": p},
			Settings:  config.Settings{MaxParallel: 4, MaxIterations: 3, OnNoUser: "next"},
		},
		Store: st,
	}
	res, err := e.Run(context.Background(), RunOptions{ResumeRunID: runID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Outputs["start"] != "S-stored" {
		t.Errorf("start re-ran: %q", res.Outputs["start"])
	}
	if res.Outputs["a"] != "A-stored" {
		t.Errorf("a re-ran: %q", res.Outputs["a"])
	}
	if res.Outputs["b"] != "B" || res.Outputs["c"] != "C" {
		t.Errorf("remaining nodes did not run: %v", res.Outputs)
	}
}
