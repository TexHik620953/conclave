package eval

import (
	"context"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/playbook"
)

func TestGoldenCases(t *testing.T) {
	registry, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), registry, GoldenCases())
	for _, r := range report.Results {
		t.Logf("case %s: passed=%v events=%d artifacts=%d err=%q", r.Case, r.Passed, r.Events, r.Artifacts, r.Err)
	}
	if report.Failed != 0 {
		t.Fatalf("%d of %d cases failed", report.Failed, report.Passed+report.Failed)
	}
	if report.Passed == 0 {
		t.Fatal("no cases ran")
	}
}

func TestBaselineCompare(t *testing.T) {
	base := Baseline{"a": true, "b": true}
	report := Report{Results: []Result{
		{Case: "a", Passed: true},
		{Case: "b", Passed: false},
		{Case: "c", Passed: true},
	}}
	regs := Compare(base, report)
	if len(regs) != 1 || regs[0] != "b" {
		t.Fatalf("regressions = %v, want [b]", regs)
	}
}
