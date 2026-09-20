package orchestrator

import (
	"context"
	"testing"

	"github.com/texhik/conclave/internal/config"
)

func testEngine(p config.Pipeline) *Engine {
	return &Engine{
		Cfg: &config.Config{
			Pipelines: map[string]config.Pipeline{"t": p},
			Settings:  config.Settings{MaxParallel: 4, MaxIterations: 3, OnNoUser: "next"},
		},
	}
}

func runTest(t *testing.T, p config.Pipeline, inputs map[string]string) *RunResult {
	t.Helper()
	e := testEngine(p)
	res, err := e.Run(context.Background(), RunOptions{Pipeline: "t", Inputs: inputs})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestConditionalEdges(t *testing.T) {
	p := config.Pipeline{
		Start: "start",
		Nodes: []config.Node{
			{ID: "start", Type: "transform", Template: "go"},
			{ID: "a", Type: "transform", Template: "A"},
			{ID: "b", Type: "transform", Template: "B"},
		},
		Edges: []config.Edge{
			{From: "start", To: "a", When: "inputs.flag"},
			{From: "start", To: "b", When: "!inputs.flag"},
		},
	}
	res := runTest(t, p, map[string]string{"flag": "true"})
	if res.Outputs["a"] != "A" {
		t.Fatalf("a output = %q, want A", res.Outputs["a"])
	}
	if _, ok := res.Outputs["b"]; ok {
		t.Fatalf("b should have been pruned, got %q", res.Outputs["b"])
	}
}

func TestDiamondJoinRunsOnce(t *testing.T) {
	p := config.Pipeline{
		Start: "start",
		Nodes: []config.Node{
			{ID: "start", Type: "transform", Template: "go"},
			{ID: "a", Type: "transform", Template: "A"},
			{ID: "b", Type: "transform", Template: "B"},
			{ID: "join", Type: "transform", Template: "A={{a}} B={{b}}"},
		},
		Edges: []config.Edge{
			{From: "start", To: "a"},
			{From: "start", To: "b"},
			{From: "a", To: "join"},
			{From: "b", To: "join"},
		},
	}
	res := runTest(t, p, nil)
	if got := res.Outputs["join"]; got != "A=A B=B" {
		t.Fatalf("join = %q, want %q", got, "A=A B=B")
	}
}

func TestGateElse(t *testing.T) {
	p := config.Pipeline{
		Start: "start",
		Nodes: []config.Node{
			{ID: "start", Type: "transform", Template: "go"},
			{ID: "g", Type: "gate", Condition: "inputs.ok", Else: "fallback"},
			{ID: "happy", Type: "transform", Template: "happy"},
			{ID: "fallback", Type: "transform", Template: "fallback"},
		},
		Edges: []config.Edge{
			{From: "start", To: "g"},
			{From: "g", To: "happy"},
		},
	}
	res := runTest(t, p, map[string]string{"ok": "false"})
	if res.Outputs["fallback"] != "fallback" {
		t.Fatalf("fallback = %q", res.Outputs["fallback"])
	}
	if _, ok := res.Outputs["happy"]; ok {
		t.Fatalf("happy should not run, got %q", res.Outputs["happy"])
	}
}

func TestGateAskNoUserPolicy(t *testing.T) {
	p := config.Pipeline{
		Start: "g",
		Nodes: []config.Node{
			{ID: "g", Type: "gate", Ask: true, Else: "fallback"},
			{ID: "happy", Type: "transform", Template: "happy"},
			{ID: "fallback", Type: "transform", Template: "fallback"},
		},
		Edges: []config.Edge{
			{From: "g", To: "happy"},
		},
	}

	for _, tc := range []struct {
		policy string
		want   string
	}{
		{"next", "happy"},
		{"else", "fallback"},
		{"stop", ""},
	} {
		e := testEngine(p)
		e.Cfg.Settings.OnNoUser = tc.policy
		res, err := e.Run(context.Background(), RunOptions{Pipeline: "t"})
		if err != nil {
			t.Fatalf("policy %s: %v", tc.policy, err)
		}
		if tc.want == "" {
			if _, ok := res.Outputs["happy"]; ok {
				t.Errorf("policy stop: happy ran")
			}
			if _, ok := res.Outputs["fallback"]; ok {
				t.Errorf("policy stop: fallback ran")
			}
			continue
		}
		if res.Outputs[tc.want] == "" {
			t.Errorf("policy %s: expected %s to run, outputs=%v", tc.policy, tc.want, res.Outputs)
		}
	}
}
