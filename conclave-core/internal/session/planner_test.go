package session

import (
	"context"
	"testing"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func TestPlannerBuildsGraph(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)
	registry, _ := playbook.LoadBuiltin()
	if err := st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Service{Store: st, Log: log}).AddSpec(ctx, "s", "# Spec\nbuild a shop"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, log, registry, nil)
	client := &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		return toolResponse("set_plan", `{
			"nodes":[
				{"id":"a","playbook_id":"ba","title":"Analyze"},
				{"id":"g","kind":"gate","title":"Approve?"},
				{"id":"b","playbook_id":"general"}
			],
			"edges":[{"from":"a","to":"g"},{"from":"a","to":"b","kind":"data"}]
		}`)
	}}
	p := &Planner{Store: st, Log: log, Playbooks: registry, Service: svc, Client: client, Model: "m"}
	plan, err := p.Build(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := st.ListPlanNodes(ctx, plan.ID)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	edges, _ := st.ListPlanEdges(ctx, plan.ID)
	if len(edges) != 2 {
		t.Fatalf("edges = %d", len(edges))
	}
	found := false
	for _, n := range nodes {
		if n.Key == "g" && n.Kind == domain.NodeKindGate {
			found = true
		}
	}
	if !found {
		t.Fatal("gate node not created")
	}
}

func TestPlannerReplans(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	log := events.New(st)
	registry, _ := playbook.LoadBuiltin()
	_ = st.CreateSession(ctx, domain.Session{ID: "s", TenantID: "t", Status: domain.SessionActive})
	if _, err := (&Service{Store: st, Log: log}).AddSpec(ctx, "s", "spec"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, log, registry, nil)
	client := &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		return toolResponse("set_plan", `{"nodes":[{"id":"a","playbook_id":"ba"},{"id":"c","playbook_id":"general"}],"edges":[{"from":"a","to":"c"}]}`)
	}}
	svc.Planner = &Planner{Store: st, Log: log, Playbooks: registry, Service: svc, Client: client, Model: "m"}

	plan1, err := svc.CreatePlan(ctx, CreatePlanInput{SessionID: "s", Nodes: []PlanNodeSpec{{ID: "a", PlaybookID: "ba"}}})
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := svc.Replan(ctx, "s", plan1.ID, "need a downstream step")
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Version != 2 {
		t.Fatalf("version = %d", plan2.Version)
	}
	nodes, _ := st.ListPlanNodes(ctx, plan2.ID)
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
}
