package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

type fakeExec struct {
	mu      sync.Mutex
	running int
	max     int
	order   []string
}

func (f *fakeExec) Execute(_ context.Context, in NodeExecInput) (*NodeResult, error) {
	f.mu.Lock()
	f.running++
	if f.running > f.max {
		f.max = f.running
	}
	f.order = append(f.order, in.Node.ID)
	f.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
	if in.Node.PlaybookID == "fail" {
		return nil, fmt.Errorf("boom")
	}
	return &NodeResult{Output: "out-" + in.Node.ID}, nil
}

type fakeGate struct {
	answered bool
	approved bool
}

func (g *fakeGate) Open(context.Context, string, domain.PlanNode) (bool, bool, error) {
	return g.answered, g.approved, nil
}

func setup(t *testing.T, nodes []domain.PlanNode, edges []domain.PlanEdge) (*memory.Store, *Scheduler, string, string) {
	t.Helper()
	st := memory.New()
	ctx := context.Background()
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{ID: "p", SessionID: "s", Version: 1, Status: domain.PlanActive}
	if err := st.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	for i := range nodes {
		nodes[i].PlanID = "p"
		if nodes[i].Kind == "" {
			nodes[i].Kind = domain.NodeKindPlaybook
		}
		nodes[i].State = domain.NodePending
		if err := st.CreatePlanNode(ctx, nodes[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range edges {
		e.PlanID = "p"
		if e.Kind == "" {
			e.Kind = domain.EdgeDependency
		}
		if err := st.CreatePlanEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	return st, &Scheduler{Store: st, Log: events.New(st), MaxParallel: 4}, "s", "p"
}

func node(id string) domain.PlanNode {
	return domain.PlanNode{ID: id, PlaybookID: "pb", Title: id}
}

func TestSchedulerParallelDiamond(t *testing.T) {
	st, sch, sid, pid := setup(t,
		[]domain.PlanNode{node("a"), node("b"), node("c"), node("d")},
		[]domain.PlanEdge{
			{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"},
		},
	)
	exec := &fakeExec{}
	sch.Executor = exec
	report, err := sch.Run(context.Background(), sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "done" {
		t.Fatalf("status = %s (%+v)", report.Status, report)
	}
	if exec.max < 2 {
		t.Fatalf("expected b and c to run in parallel, max concurrency = %d", exec.max)
	}
	// d must run last.
	if exec.order[len(exec.order)-1] != "d" {
		t.Fatalf("execution order = %v, want d last", exec.order)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		n, _ := st.GetPlanNode(context.Background(), id)
		if n.State != domain.NodeDone {
			t.Errorf("node %s state = %s", id, n.State)
		}
		if n.Output == "" {
			t.Errorf("node %s has no output", id)
		}
	}
}

func TestSchedulerGateDoesNotBlockSiblings(t *testing.T) {
	st, sch, sid, pid := setup(t,
		[]domain.PlanNode{
			node("a"),
			{ID: "g", Kind: domain.NodeKindGate, Title: "Approve?"},
			node("b"),
		},
		[]domain.PlanEdge{{From: "a", To: "g"}, {From: "a", To: "b"}},
	)
	sch.Executor = &fakeExec{}
	sch.Gates = &fakeGate{answered: false}
	report, err := sch.Run(context.Background(), sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "waiting" || len(report.Waiting) != 1 || report.Waiting[0] != "g" {
		t.Fatalf("report = %+v, want waiting on g", report)
	}
	// Sibling b must have completed despite the gate waiting.
	b, _ := st.GetPlanNode(context.Background(), "b")
	if b.State != domain.NodeDone {
		t.Fatalf("sibling b state = %s, want done", b.State)
	}
	g, _ := st.GetPlanNode(context.Background(), "g")
	if g.State != domain.NodeWaitingUser {
		t.Fatalf("gate state = %s, want waiting_user", g.State)
	}

	// Resolve the gate and resume: the plan completes.
	if err := st.UpdatePlanNodeState(context.Background(), "g", domain.NodeDone); err != nil {
		t.Fatal(err)
	}
	report, err = sch.Run(context.Background(), sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "done" {
		t.Fatalf("resumed status = %s", report.Status)
	}
}

func TestSchedulerAutoApprovedGate(t *testing.T) {
	st, sch, sid, pid := setup(t,
		[]domain.PlanNode{node("a"), {ID: "g", Kind: domain.NodeKindGate, Title: "ok?"}},
		[]domain.PlanEdge{{From: "a", To: "g"}},
	)
	sch.Executor = &fakeExec{}
	sch.Gates = &fakeGate{answered: true, approved: true}
	report, err := sch.Run(context.Background(), sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "done" {
		t.Fatalf("status = %s", report.Status)
	}
	g, _ := st.GetPlanNode(context.Background(), "g")
	if g.State != domain.NodeDone {
		t.Fatalf("gate state = %s", g.State)
	}
}

func TestSchedulerFailureCascades(t *testing.T) {
	st, sch, sid, pid := setup(t,
		[]domain.PlanNode{node("a"), node("b")},
		[]domain.PlanEdge{{From: "a", To: "b"}},
	)
	// Executor fails node "a".
	sch.Executor = &selectiveFail{failID: "a"}
	report, err := sch.Run(context.Background(), sid, pid)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" {
		t.Fatalf("status = %s (%+v)", report.Status, report)
	}
	b, _ := st.GetPlanNode(context.Background(), "b")
	if b.State != domain.NodeCancelled {
		t.Fatalf("dependent b state = %s, want cancelled", b.State)
	}
}

type selectiveFail struct{ failID string }

func (s *selectiveFail) Execute(_ context.Context, in NodeExecInput) (*NodeResult, error) {
	if in.Node.ID == s.failID {
		return nil, fmt.Errorf("boom")
	}
	return &NodeResult{Output: "ok"}, nil
}
