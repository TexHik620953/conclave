// Package eval is the evaluation harness: it runs golden tasks against
// playbooks/plans and checks the produced artifacts, events and states. It is
// the "tests for the agent" contour and is intentionally separate from the
// playbook template.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/session"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

// Case is one golden task.
type Case struct {
	Name       string
	PlaybookID string
	Idea       string
	// Decisions script the supervisor brain; Finish is the final summary.
	Decisions []orchestrator.Decision
	Finish    string
	// RoleOutput overrides role output.
	RoleOutput func(in orchestrator.RoleInput) string
	// Brain overrides the scripted decisions entirely.
	Brain func(in orchestrator.DecisionInput) orchestrator.Decision
	// Critic scripts critic verdicts (optional).
	Critic []orchestrator.CriticVerdict

	// Plan, when set, runs a plan graph instead of a single playbook.
	Plan  []session.PlanNodeSpec
	Edges []session.PlanEdgeSpec

	// Session options.
	AutoAccept bool

	// Expectations.
	ExpectArtifact   string
	ExpectContains   []string
	ExpectEvents     []string
	ExpectPlanStatus string
	ExpectQuestions  int
}

// Result is the outcome of one case.
type Result struct {
	Case      string        `json:"case"`
	Passed    bool          `json:"passed"`
	Duration  time.Duration `json:"duration"`
	Err       string        `json:"error,omitempty"`
	Events    int           `json:"events"`
	Artifacts int           `json:"artifacts"`
}

// Report aggregates all case results.
type Report struct {
	Results  []Result      `json:"results"`
	Passed   int           `json:"passed"`
	Failed   int           `json:"failed"`
	Duration time.Duration `json:"duration"`
}

// Run executes every case against the given playbook registry.
func Run(ctx context.Context, registry *playbook.Registry, cases []Case) Report {
	report := Report{}
	start := time.Now()
	for _, c := range cases {
		r := runCase(ctx, registry, c)
		report.Results = append(report.Results, r)
		if r.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	report.Duration = time.Since(start)
	return report
}

// Baseline maps case names to their previous pass/fail state.
type Baseline map[string]bool

// LoadBaseline reads a baseline from a JSON report or baseline file.
func LoadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b := Baseline{}
	if err := json.Unmarshal(data, &b); err == nil && len(b) > 0 {
		return b, nil
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("eval: parse baseline: %w", err)
	}
	for _, r := range rep.Results {
		b[r.Case] = r.Passed
	}
	return b, nil
}

// SaveBaseline writes the current report as a baseline.
func SaveBaseline(path string, report Report) error {
	b := Baseline{}
	for _, r := range report.Results {
		b[r.Case] = r.Passed
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Compare returns case names that regressed (passed before, fail now).
func Compare(base Baseline, report Report) []string {
	var regressions []string
	for _, r := range report.Results {
		if was, ok := base[r.Case]; ok && was && !r.Passed {
			regressions = append(regressions, r.Case)
		}
	}
	sort.Strings(regressions)
	return regressions
}

func runCase(ctx context.Context, registry *playbook.Registry, c Case) Result {
	res := Result{Case: c.Name}
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()

	fail := func(format string, args ...any) Result {
		res.Err = fmt.Sprintf(format, args...)
		return res
	}

	st := memory.New()
	log := events.New(st)
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "eval"}); err != nil {
		return fail("create tenant: %v", err)
	}
	roles := &orchestrator.FakeRoleRunner{}
	if c.RoleOutput != nil {
		roles.Fn = c.RoleOutput
	}
	sup := &orchestrator.Supervisor{
		Brain:      &orchestrator.FakeBrain{Decisions: c.Decisions, Finish: c.Finish, Fn: c.Brain},
		Roles:      roles,
		Log:        log,
		MaxSteps:   8,
		Dispatcher: orchestrator.NewDispatcher(),
	}
	if len(c.Critic) > 0 {
		sup.Critic = &orchestrator.FakeCritic{Verdicts: c.Critic}
		sup.MaxReopens = 2
	}
	svc := session.NewService(st, log, registry, sup)

	sess, err := svc.CreateSession(ctx, session.CreateSessionInput{TenantID: "eval", Title: c.Name, AutoAccept: c.AutoAccept})
	if err != nil {
		return fail("create session: %v", err)
	}

	planStatus := ""
	if len(c.Plan) > 0 {
		plan, err := svc.CreatePlan(ctx, session.CreatePlanInput{SessionID: sess.ID, Nodes: c.Plan, Edges: c.Edges})
		if err != nil {
			return fail("create plan: %v", err)
		}
		report, err := svc.RunPlan(ctx, sess.ID, plan.ID)
		if err != nil {
			return fail("run plan: %v", err)
		}
		if report != nil {
			planStatus = report.Status
		}
	} else {
		if _, err := svc.RunPlaybook(ctx, session.RunInput{SessionID: sess.ID, PlaybookID: c.PlaybookID, Idea: c.Idea}); err != nil {
			return fail("run playbook: %v", err)
		}
	}

	arts, _ := st.ListArtifacts(ctx, sess.ID)
	evs, _ := log.List(ctx, sess.ID, 0)
	questions, _ := st.ListQuestions(ctx, sess.ID)
	res.Artifacts = len(arts)
	res.Events = len(evs)

	if c.ExpectPlanStatus != "" && planStatus != c.ExpectPlanStatus {
		return fail("plan status = %q, want %q", planStatus, c.ExpectPlanStatus)
	}
	if c.ExpectArtifact != "" {
		found := false
		for _, a := range arts {
			if a.Name == c.ExpectArtifact {
				found = true
				for _, want := range c.ExpectContains {
					if !strings.Contains(a.Ref, want) {
						return fail("artifact %s does not contain %q", a.Name, want)
					}
				}
			}
		}
		if !found {
			return fail("expected artifact %q, got %v", c.ExpectArtifact, artifactNames(arts))
		}
	}
	if c.ExpectQuestions > 0 {
		open := 0
		for _, q := range questions {
			if q.State == domain.QuestionOpen {
				open++
			}
		}
		if open != c.ExpectQuestions {
			return fail("open questions = %d, want %d", open, c.ExpectQuestions)
		}
	}
	for _, want := range c.ExpectEvents {
		if !hasEvent(evs, want) {
			return fail("expected event %q", want)
		}
	}
	res.Passed = true
	return res
}

func artifactNames(arts []domain.Artifact) []string {
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.Name)
	}
	return out
}

func hasEvent(evs []domain.Event, typ string) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}
