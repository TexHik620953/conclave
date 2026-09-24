package orchestrator

import (
	"context"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func TestSupervisorCriticReopensAndEscalates(t *testing.T) {
	st := memory.New()
	log := events.New(st)
	roles := &FakeRoleRunner{Fn: func(in RoleInput) string { return "work by " + in.Role.Tier }}
	brain := &FakeBrain{Fn: func(in DecisionInput) Decision {
		if len(in.History) == 0 {
			return Decision{Delegate: &Delegation{Role: "auto", Task: "do the work"}}
		}
		if in.History[len(in.History)-1].Role == "critic" {
			return Decision{Delegate: &Delegation{Role: "auto", Task: "fix it"}}
		}
		return Decision{Finish: "attempt result"}
	}}
	critic := &FakeCritic{Verdicts: []CriticVerdict{
		{Pass: false, Feedback: "missing tests", SuggestedTier: "senior"},
		{Pass: true},
	}}
	sup := &Supervisor{
		Brain: brain, Roles: roles, Log: log, MaxSteps: 6,
		Critic: critic, Dispatcher: NewDispatcher(), MaxReopens: 2,
	}
	pb := playbook.Playbook{
		ID: "ba", Version: 1, Title: "BA",
		Roles: []playbook.Role{{Tier: "junior", Model: "m"}, {Tier: "senior", Model: "m"}},
	}
	res, err := sup.Run(context.Background(), RunInput{SessionID: "s", PlaybookRunID: "r", Playbook: pb})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "attempt result" {
		t.Fatalf("output = %q", res.Output)
	}
	// First delegation resolved "auto" to the weakest tier (junior).
	if len(roles.Calls()) < 2 {
		t.Fatalf("expected at least 2 role runs, got %d", len(roles.Calls()))
	}
	if roles.Calls()[0].Role.Tier != "junior" {
		t.Fatalf("first tier = %s, want junior", roles.Calls()[0].Role.Tier)
	}
	// After critic feedback the minimum tier escalated to senior.
	if roles.Calls()[1].Role.Tier != "senior" {
		t.Fatalf("second tier = %s, want senior", roles.Calls()[1].Role.Tier)
	}
	if len(critic.Calls()) != 2 {
		t.Fatalf("critic calls = %d, want 2", len(critic.Calls()))
	}
}

func TestSupervisorCriticPassesImmediately(t *testing.T) {
	st := memory.New()
	sup := &Supervisor{
		Brain:      &FakeBrain{Finish: "done"},
		Roles:      &FakeRoleRunner{},
		Log:        events.New(st),
		MaxSteps:   3,
		Critic:     &FakeCritic{},
		Dispatcher: NewDispatcher(),
	}
	pb := playbook.Playbook{ID: "ba", Version: 1, Roles: []playbook.Role{{Tier: "senior", Model: "m"}}}
	res, err := sup.Run(context.Background(), RunInput{SessionID: "s", PlaybookRunID: "r", Playbook: pb})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "done" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestDispatcherEscalation(t *testing.T) {
	d := NewDispatcher()
	if d.Rank("senior") <= d.Rank("junior") {
		t.Fatal("senior should outrank junior")
	}
	if d.Escalate("junior") != "middle" || d.Escalate("senior") != "lead" {
		t.Fatalf("escalation wrong: %s %s", d.Escalate("junior"), d.Escalate("senior"))
	}
	if d.AtLeast("middle", "senior") != "senior" {
		t.Fatal("AtLeast failed")
	}
}
