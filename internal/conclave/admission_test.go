package conclave

import (
	"testing"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
)

func TestFindCycle(t *testing.T) {
	tasks := []domain.Task{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	if got := findCycle(tasks); got == "" {
		t.Fatal("expected a cycle")
	}
	tasks[2].DependsOn = nil
	if got := findCycle(tasks); got != "" {
		t.Fatalf("unexpected cycle: %s", got)
	}
}

func TestAdmissionLimits(t *testing.T) {
	p := config.Profile{
		Workers: []config.WorkerConfig{{ID: "w", Instances: 2}},
		Limits:  config.Limits{MaxParallel: 3},
	}
	cfg := &config.Config{Providers: map[string]config.Provider{
		"p": {MaxParallel: 1, Models: map[string]config.Model{"m": {Context: 100}}},
	}}
	a := newAdmission(p, cfg)
	if !a.tryAcquire("p", "w") {
		t.Fatal("first acquire should succeed")
	}
	if a.tryAcquire("p", "w") {
		t.Fatal("provider/worker limit was not enforced")
	}
	a.release("p", "w")
	if !a.tryAcquire("p", "w") {
		t.Fatal("slot was not released")
	}
}
