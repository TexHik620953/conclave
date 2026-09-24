package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/scheduler"
	"github.com/texhik/conclave/conclave-core/internal/session"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func setup(t *testing.T) (*memory.Store, *session.Service, *events.Log) {
	t.Helper()
	st := memory.New()
	log := events.New(st)
	registry, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	sup := &orchestrator.Supervisor{
		Brain:    &orchestrator.FakeBrain{Finish: "done"},
		Roles:    &orchestrator.FakeRoleRunner{},
		Log:      log,
		MaxSteps: 5,
	}
	svc := session.NewService(st, log, registry, sup)
	return st, svc, log
}

func newPlan(t *testing.T, svc *session.Service, st *memory.Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(ctx, session.CreateSessionInput{TenantID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.CreatePlan(ctx, session.CreatePlanInput{
		SessionID: sess.ID, Nodes: []session.PlanNodeSpec{{ID: "a", PlaybookID: "ba"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID, plan.ID
}

func TestWorkerRunsQueuedJob(t *testing.T) {
	ctx := context.Background()
	st, svc, log := setup(t)
	sid, pid := newPlan(t, svc, st)
	job, err := svc.EnqueuePlan(ctx, sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: st, Service: svc, Log: log, ID: "w1", Lease: time.Minute}
	did, err := w.RunOnce(ctx)
	if err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	j, _ := st.GetJob(ctx, job.ID)
	if j.State != domain.JobDone {
		t.Fatalf("job state = %s", j.State)
	}
	n, _ := st.GetPlanNodeByKey(ctx, pid, "a")
	if n.State != domain.NodeDone {
		t.Fatalf("node state = %s", n.State)
	}
	// Nothing left to do.
	if did, _ := w.RunOnce(ctx); did {
		t.Fatal("expected no more jobs")
	}
}

func TestWorkerReconcilesStuckNode(t *testing.T) {
	ctx := context.Background()
	st, svc, log := setup(t)
	sid, pid := newPlan(t, svc, st)
	// Simulate a crashed worker: node left running.
	node, err := st.GetPlanNodeByKey(ctx, pid, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePlanNodeState(ctx, node.ID, domain.NodeRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnqueuePlan(ctx, sid, pid); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: st, Service: svc, Log: log, ID: "w1", Lease: time.Minute}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	n, _ := st.GetPlanNodeByKey(ctx, pid, "a")
	if n.State != domain.NodeDone {
		t.Fatalf("node state = %s, want done", n.State)
	}
}

func TestWorkerReclaimsExpiredLease(t *testing.T) {
	ctx := context.Background()
	st, svc, _ := setup(t)
	sid, pid := newPlan(t, svc, st)
	job, err := svc.EnqueuePlan(ctx, sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimJob(ctx, "w1", time.Millisecond)
	if err != nil || first == nil || first.ID != job.ID {
		t.Fatalf("first claim = %+v err=%v", first, err)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := st.ClaimJob(ctx, "w2", time.Minute)
	if err != nil || second == nil {
		t.Fatalf("reclaim = %+v err=%v", second, err)
	}
	if second.ID != job.ID || second.LeasedBy != "w2" || second.Attempts != 2 {
		t.Fatalf("reclaimed job = %+v", second)
	}
	// The original worker can no longer heartbeat the job.
	if err := st.HeartbeatJob(ctx, job.ID, "w1", time.Minute); err == nil {
		t.Fatal("expected heartbeat from a stale worker to fail")
	}
}

func TestWorkerRequeuesOnFailure(t *testing.T) {
	ctx := context.Background()
	st, svc, log := setup(t)
	sid, pid := newPlan(t, svc, st)
	// Remove the executor so RunPlan fails.
	svc.Scheduler.Executor = nil
	job, err := svc.EnqueuePlan(ctx, sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: st, Service: svc, Log: log, ID: "w1", Lease: time.Minute, MaxAttempts: 3}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	j, _ := st.GetJob(ctx, job.ID)
	if j.State != domain.JobQueued || j.Attempts != 1 {
		t.Fatalf("job = %+v, want requeued with 1 attempt", j)
	}
}

type blockingExec struct {
	once    sync.Once
	started chan struct{}
}

func (b *blockingExec) Execute(ctx context.Context, _ scheduler.NodeExecInput) (*scheduler.NodeResult, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestWorkerCancelRequestsStopsJob(t *testing.T) {
	ctx := context.Background()
	st, svc, log := setup(t)
	sid, pid := newPlan(t, svc, st)
	job, err := svc.EnqueuePlan(ctx, sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	block := &blockingExec{started: make(chan struct{})}
	svc.Scheduler.Executor = block

	w := &Worker{
		Store: st, Service: svc, Log: log, ID: "w1",
		Lease: time.Minute, HeartbeatInterval: 10 * time.Millisecond, MaxAttempts: 3,
	}
	done := make(chan struct{})
	go func() {
		_, _ = w.RunOnce(ctx)
		close(done)
	}()

	select {
	case <-block.started:
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not start")
	}
	if err := st.RequestJobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
	j, _ := st.GetJob(ctx, job.ID)
	if j.State != domain.JobCancelled {
		t.Fatalf("job state = %s, want cancelled", j.State)
	}
}
