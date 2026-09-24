package orchestrator

import (
	"context"
	"sync"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

type fakeArtifacts struct {
	mu    sync.Mutex
	items map[string]string
}

func (f *fakeArtifacts) Write(_ context.Context, _, _, name, content string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items == nil {
		f.items = map[string]string{}
	}
	f.items[name] = content
	return "ref:" + name, nil
}

func TestSupervisorDelegatesAndFinishes(t *testing.T) {
	st := memory.New()
	log := events.New(st)
	arts := &fakeArtifacts{}

	brain := &FakeBrain{
		Decisions: []Decision{
			{Delegate: &Delegation{Role: "senior", Task: "collect requirements"}},
		},
		Finish: "requirements complete",
	}
	roles := &FakeRoleRunner{Fn: func(in RoleInput) string { return "REQ: " + in.Task }}

	sup := &Supervisor{Brain: brain, Roles: roles, Artifacts: arts, Log: log, MaxSteps: 5}
	pb := playbook.Playbook{
		ID: "ba", Version: 1, Title: "Business Analysis",
		Outputs: []string{"requirements.md"},
		Roles:   []playbook.Role{{Tier: "senior", Model: "m"}},
	}
	res, err := sup.Run(context.Background(), RunInput{
		SessionID: "s1", PlaybookRunID: "r1", Playbook: pb, Spec: "build a shop",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "requirements complete" {
		t.Fatalf("output = %q", res.Output)
	}
	if len(res.Steps) != 1 || res.Steps[0].Output != "REQ: collect requirements" {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0] != "requirements.md" {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
	if arts.items["requirements.md"] != "requirements complete" {
		t.Fatalf("artifact content = %q", arts.items["requirements.md"])
	}

	evs, err := log.List(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"playbook.started": false, "task.created": false, "task.started": false,
		"task.finished": false, "artifact.created": false, "playbook.finished": false,
	}
	for _, e := range evs {
		if _, ok := want[e.Type]; ok {
			want[e.Type] = true
		}
	}
	for typ, ok := range want {
		if !ok {
			t.Errorf("missing event %s", typ)
		}
	}
}

func TestSupervisorUnknownRoleFails(t *testing.T) {
	st := memory.New()
	sup := &Supervisor{
		Brain:    &FakeBrain{Decisions: []Decision{{Delegate: &Delegation{Role: "nope", Task: "x"}}}},
		Roles:    &FakeRoleRunner{},
		Log:      events.New(st),
		MaxSteps: 3,
	}
	pb := playbook.Playbook{ID: "ba", Version: 1, Roles: []playbook.Role{{Tier: "senior", Model: "m"}}}
	if _, err := sup.Run(context.Background(), RunInput{SessionID: "s1", PlaybookRunID: "r1", Playbook: pb}); err == nil {
		t.Fatal("expected error for unknown role tier")
	}
}
