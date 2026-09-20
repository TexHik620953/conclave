package orchestrator

import "testing"

func TestEval(t *testing.T) {
	st := NewState("r", "do it", "/tmp")
	st.SetOutput("review", "Looks good.\nSTATUS: PASS")
	st.SetOutput("bad", "There is a problem.\nSTATUS: FAIL")
	st.SetArtifact("plan.md", "the plan")

	cases := []struct {
		expr string
		iter int
		want bool
	}{
		{"review.passed", 0, true},
		{"bad.passed", 0, false},
		{"bad.failed", 0, true},
		{"review.nonempty", 0, true},
		{"missing.empty", 0, true},
		{"iterations >= 3", 3, true},
		{"iterations < 3", 3, false},
		{"review.passed && iterations >= 1", 1, true},
		{"bad.passed || review.passed", 0, true},
		{"!bad.passed", 0, true},
		{"artifacts.plan.md contains plan", 0, true},
		{"outputs.review contains STATUS", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := Eval(tc.expr, st, tc.iter)
		if err != nil {
			t.Fatalf("Eval(%q) error: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("Eval(%q, iter=%d) = %v, want %v", tc.expr, tc.iter, got, tc.want)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	st := NewState("r", "build it", "/tmp")
	st.SetOutput("plan", "the plan body")
	st.SetArtifact("plan.md", "artifact body")

	got := renderTemplate("Task: {{task}}\nPlan: {{plan}}\nArt: {{artifacts.plan.md}}", st)
	want := "Task: build it\nPlan: the plan body\nArt: artifact body"
	if got != want {
		t.Fatalf("renderTemplate = %q, want %q", got, want)
	}
}
