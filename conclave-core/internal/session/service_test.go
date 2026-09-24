package session

import (
	"context"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

// TestEndToEndDeviceSessionPlaybook exercises the P0 flow:
// device token -> authenticate -> session -> spec -> playbook -> events/artifact.
func TestEndToEndDeviceSessionPlaybook(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)

	// Identity.
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t1", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, domain.User{ID: "u1", TenantID: "t1", Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.New(st, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	token, dev, err := mgr.RegisterDevice(ctx, "t1", "u1", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	authed, err := mgr.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if authed.ID != dev.ID || authed.TenantID != "t1" {
		t.Fatalf("unexpected device: %+v", authed)
	}
	if _, err := mgr.Authenticate(ctx, "cc_wrong"); err == nil {
		t.Fatal("expected unauthorized for a bad token")
	}

	// Playbook + supervisor with deterministic fakes.
	registry, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	sup := &orchestrator.Supervisor{
		Brain: &orchestrator.FakeBrain{
			Decisions: []orchestrator.Decision{{Delegate: &orchestrator.Delegation{Role: "senior", Task: "clarify goals"}}},
			Finish:    "# Requirements\n- goal: sell online",
		},
		Roles:    &orchestrator.FakeRoleRunner{},
		Log:      log,
		MaxSteps: 5,
	}
	svc := NewService(st, log, registry, sup)

	sess, err := svc.CreateSession(ctx, CreateSessionInput{TenantID: "t1", Title: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.RunPlaybook(ctx, RunInput{SessionID: sess.ID, PlaybookID: "ba", Idea: "build an online shop"})
	if err != nil {
		t.Fatalf("RunPlaybook: %v", err)
	}
	if out.Status != domain.NodeDone {
		t.Fatalf("status = %s", out.Status)
	}

	arts, err := st.ListArtifacts(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Name != "requirements.md" {
		t.Fatalf("artifacts = %+v", arts)
	}
	if arts[0].Ref == "" {
		t.Fatal("artifact content is empty")
	}

	evs, err := log.List(ctx, sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("no events recorded")
	}
	// Sequence must be strictly increasing (resume relies on it).
	for i := 1; i < len(evs); i++ {
		if evs[i].Seq <= evs[i-1].Seq {
			t.Fatalf("event seq not increasing: %d then %d", evs[i-1].Seq, evs[i].Seq)
		}
	}

	// Resume from the middle returns only later events.
	mid := evs[len(evs)/2].Seq
	tail, err := log.List(ctx, sess.ID, mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != len(evs)-len(evs)/2-1 {
		t.Fatalf("resume returned %d events, want %d", len(tail), len(evs)-len(evs)/2-1)
	}
}

func TestCreateSessionRequiresTenant(t *testing.T) {
	st := memory.New()
	svc := NewService(st, events.New(st), playbook.NewRegistry(), nil)
	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{}); err == nil {
		t.Fatal("expected error without tenant")
	}
}
