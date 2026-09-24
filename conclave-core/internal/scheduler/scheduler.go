// Package scheduler executes a plan graph: it runs ready nodes in parallel up
// to a quota, waits on dependencies, opens human gates without blocking
// unrelated branches, and is resumable from persisted node states.
package scheduler

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/obs"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// NodeExecutor runs a playbook node.
type NodeExecutor interface {
	Execute(ctx context.Context, in NodeExecInput) (*NodeResult, error)
}

// NodeExecInput is what a node executor receives.
type NodeExecInput struct {
	SessionID string
	PlanID    string
	Node      domain.PlanNode
	Spec      string
	// Inputs are the outputs of predecessors connected by data edges.
	Inputs []string
}

// NodeResult is what a node executor returns.
type NodeResult struct {
	Output    string
	Artifacts []string
}

// Gatekeeper opens human gates and reports whether they were auto-resolved.
type Gatekeeper interface {
	// Open creates a question for the gate node. answered=true means the gate
	// was resolved immediately (auto-accept); approved is the decision.
	Open(ctx context.Context, sessionID string, node domain.PlanNode) (answered, approved bool, err error)
}

// Scheduler executes plans.
type Scheduler struct {
	Store       store.Store
	Log         *events.Log
	Executor    NodeExecutor
	Gates       Gatekeeper
	MaxParallel int
}

// RunReport summarizes a scheduler pass.
type RunReport struct {
	SessionID string   `json:"session_id"`
	PlanID    string   `json:"plan_id"`
	Status    string   `json:"status"` // done | waiting | failed
	Waiting   []string `json:"waiting,omitempty"`
	Failed    []string `json:"failed,omitempty"`
}

type nodeOutcome struct {
	state  domain.NodeState
	output string
	err    error
}

// Run executes the plan until no more nodes can progress without user input.
func (s *Scheduler) Run(ctx context.Context, sessionID, planID string) (*RunReport, error) {
	ctx, span := obs.Span(ctx, "scheduler.run",
		attribute.String("session_id", sessionID), attribute.String("plan_id", planID))
	defer span.End()

	spec := ""
	if sp, err := s.Store.LatestSpec(ctx, sessionID); err == nil {
		spec = sp.Content
	}

	for {
		nodes, err := s.Store.ListPlanNodes(ctx, planID)
		if err != nil {
			return nil, err
		}
		edges, err := s.Store.ListPlanEdges(ctx, planID)
		if err != nil {
			return nil, err
		}
		byKey := map[string]domain.PlanNode{}
		state := map[string]domain.NodeState{}
		output := map[string]string{}
		for _, n := range nodes {
			byKey[n.Key] = n
			state[n.Key] = n.State
			output[n.Key] = n.Output
		}
		preds := map[string][]domain.PlanEdge{}
		for _, e := range edges {
			preds[e.To] = append(preds[e.To], e)
		}

		// Cascade cancellation to successors of failed nodes.
		changed, err := s.cancelOrphans(ctx, nodes, preds, state)
		if err != nil {
			return nil, err
		}
		if changed {
			continue
		}

		ready := make([]domain.PlanNode, 0, len(nodes))
		for _, n := range nodes {
			if n.State != domain.NodePending && n.State != domain.NodeReady {
				continue
			}
			if allTerminal(preds[n.Key], state) {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			break
		}

		// Gather data-edge inputs for each ready node.
		inputs := map[string][]string{}
		for _, n := range ready {
			for _, e := range preds[n.Key] {
				if e.Kind == domain.EdgeData {
					inputs[n.Key] = append(inputs[n.Key], output[e.From])
				}
			}
		}

		max := s.MaxParallel
		if max <= 0 {
			max = 4
		}
		sem := make(chan struct{}, max)
		results := make(map[string]nodeOutcome, len(ready))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, n := range ready {
			wg.Add(1)
			go func(n domain.PlanNode) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if err := s.Store.UpdatePlanNodeState(ctx, n.ID, domain.NodeRunning); err != nil {
					mu.Lock()
					results[n.Key] = nodeOutcome{state: n.State, err: err}
					mu.Unlock()
					return
				}
				out := s.execNode(ctx, sessionID, planID, n, spec, inputs[n.Key])
				mu.Lock()
				results[n.Key] = out
				mu.Unlock()
			}(n)
		}
		wg.Wait()

		for key, out := range results {
			node := byKey[key]
			if err := s.Store.UpdatePlanNodeState(ctx, node.ID, out.state); err != nil {
				return nil, err
			}
			if out.output != "" {
				if err := s.Store.UpdatePlanNodeOutput(ctx, node.ID, out.output); err != nil {
					return nil, err
				}
			}
			obs.Default().Counter("conclave_scheduler_nodes_total", "Scheduler node outcomes", obs.Labels{
				"kind": node.Kind, "state": string(out.state),
			}).Inc()
			payload := map[string]any{
				"node_id": node.ID, "key": key, "state": string(out.state),
				"title": node.Title, "role": node.PlaybookID,
			}
			if out.err != nil {
				payload["error"] = out.err.Error()
			}
			if err := s.emit(ctx, sessionID, domain.EventNodeStateChanged, payload); err != nil {
				return nil, err
			}
			if out.err != nil {
				_ = s.emit(ctx, sessionID, domain.EventError, map[string]any{
					"node_id": node.ID, "key": key, "error": out.err.Error(),
				})
			}
		}
	}

	// Finalize.
	nodes, err := s.Store.ListPlanNodes(ctx, planID)
	if err != nil {
		return nil, err
	}
	report := &RunReport{SessionID: sessionID, PlanID: planID, Status: "done"}
	for _, n := range nodes {
		switch n.State {
		case domain.NodeWaitingUser:
			report.Status = "waiting"
			report.Waiting = append(report.Waiting, n.Key)
		case domain.NodeFailed:
			report.Status = "failed"
			report.Failed = append(report.Failed, n.Key)
		}
	}
	if report.Status == "done" {
		_ = s.Store.UpdatePlanStatus(ctx, planID, domain.PlanDone)
	} else if report.Status == "failed" {
		_ = s.Store.UpdatePlanStatus(ctx, planID, domain.PlanFailed)
	}
	obs.Default().Counter("conclave_plans_total", "Plan runs by status", obs.Labels{"status": report.Status}).Inc()
	return report, nil
}

func (s *Scheduler) execNode(ctx context.Context, sessionID, planID string, n domain.PlanNode, spec string, inputs []string) nodeOutcome {
	if n.Kind == domain.NodeKindGate {
		return s.execGate(ctx, sessionID, n)
	}
	if s.Executor == nil {
		return nodeOutcome{state: domain.NodeFailed, err: fmt.Errorf("scheduler: no executor configured")}
	}
	result, err := s.Executor.Execute(ctx, NodeExecInput{
		SessionID: sessionID, PlanID: planID, Node: n, Spec: spec, Inputs: inputs,
	})
	if err != nil {
		return nodeOutcome{state: domain.NodeFailed, err: err}
	}
	return nodeOutcome{state: domain.NodeDone, output: result.Output}
}

func (s *Scheduler) execGate(ctx context.Context, sessionID string, n domain.PlanNode) nodeOutcome {
	if s.Gates == nil {
		return nodeOutcome{state: domain.NodeDone}
	}
	answered, approved, err := s.Gates.Open(ctx, sessionID, n)
	if err != nil {
		return nodeOutcome{state: domain.NodeFailed, err: err}
	}
	if !answered {
		return nodeOutcome{state: domain.NodeWaitingUser}
	}
	if approved {
		return nodeOutcome{state: domain.NodeDone}
	}
	return nodeOutcome{state: domain.NodeFailed}
}

// cancelOrphans cancels pending/ready nodes whose predecessor failed.
func (s *Scheduler) cancelOrphans(ctx context.Context, nodes []domain.PlanNode, preds map[string][]domain.PlanEdge, state map[string]domain.NodeState) (bool, error) {
	changed := false
	for _, n := range nodes {
		if n.State != domain.NodePending && n.State != domain.NodeReady {
			continue
		}
		for _, e := range preds[n.Key] {
			if state[e.From] == domain.NodeFailed || state[e.From] == domain.NodeCancelled {
				if err := s.Store.UpdatePlanNodeState(ctx, n.ID, domain.NodeCancelled); err != nil {
					return false, err
				}
				state[n.Key] = domain.NodeCancelled
				changed = true
				break
			}
		}
	}
	return changed, nil
}

func allTerminal(edges []domain.PlanEdge, state map[string]domain.NodeState) bool {
	for _, e := range edges {
		st := state[e.From]
		if st != domain.NodeDone && st != domain.NodeCancelled {
			return false
		}
	}
	return true
}

func (s *Scheduler) emit(ctx context.Context, sessionID, typ string, payload map[string]any) error {
	if s.Log == nil {
		return nil
	}
	_, err := s.Log.Append(ctx, domain.Event{SessionID: sessionID, Type: typ, Payload: payload})
	return err
}
