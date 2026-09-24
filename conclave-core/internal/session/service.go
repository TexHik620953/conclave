// Package session wires the store, event log, playbook registry and
// orchestrator into the session lifecycle used by the API.
package session

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/scheduler"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

// Service is the application layer for sessions.
type Service struct {
	Store       store.Store
	Log         *events.Log
	Playbooks   playbook.Provider
	Supervisor  *orchestrator.Supervisor
	Scheduler   *scheduler.Scheduler
	Interviewer *Interviewer
	Planner     *Planner
	Controller  *Controller
	// MaxPlanVersions caps re-planning to avoid churn (default 5).
	MaxPlanVersions int
	// DefaultBudget is applied to sessions created without an explicit budget.
	DefaultBudget float64
	// DeltaSink receives token deltas from role runs (optional).
	DeltaSink stream.Sink
}

// CreateSessionInput describes a new session.
type CreateSessionInput struct {
	TenantID   string
	ProjectID  string
	Title      string
	AutoAccept bool
	BudgetUSD  float64
}

// CreateSession creates a session and emits session.created.
func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (*domain.Session, error) {
	if in.TenantID == "" {
		return nil, fmt.Errorf("session: tenant_id is required")
	}
	if in.BudgetUSD == 0 {
		in.BudgetUSD = s.DefaultBudget
	}
	sess := domain.Session{
		ID:         uuid.NewString(),
		TenantID:   in.TenantID,
		ProjectID:  in.ProjectID,
		Title:      in.Title,
		Status:     domain.SessionActive,
		AutoAccept: in.AutoAccept,
		BudgetUSD:  in.BudgetUSD,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sess.ID, Type: domain.EventSessionCreated,
		Payload: map[string]any{"title": sess.Title, "tenant_id": sess.TenantID},
	}); err != nil {
		return nil, err
	}
	return &sess, nil
}

// AddSpec stores a new spec version for a session.
func (s *Service) AddSpec(ctx context.Context, sessionID, content string) (*domain.Spec, error) {
	version := 1
	if prev, err := s.Store.LatestSpec(ctx, sessionID); err == nil {
		version = prev.Version + 1
	} else if err != store.ErrNotFound {
		return nil, err
	}
	spec := domain.Spec{
		ID: uuid.NewString(), SessionID: sessionID, Version: version,
		Content: content, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreateSpec(ctx, spec); err != nil {
		return nil, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventSpecCreated,
		Payload: map[string]any{"version": version},
	}); err != nil {
		return nil, err
	}
	return &spec, nil
}

// RunInput starts a playbook for a session.
type RunInput struct {
	SessionID  string
	PlaybookID string
	// Idea is used as the spec when the session has none yet.
	Idea string
}

// RunOutcome summarizes a playbook execution.
type RunOutcome struct {
	SessionID     string                  `json:"session_id"`
	PlanID        string                  `json:"plan_id"`
	PlanNodeID    string                  `json:"plan_node_id"`
	PlaybookRunID string                  `json:"playbook_run_id"`
	Status        domain.NodeState        `json:"status"`
	Result        *orchestrator.RunResult `json:"result,omitempty"`
}

// RunPlaybook creates a one-node plan, runs the supervisor and records state.
func (s *Service) RunPlaybook(ctx context.Context, in RunInput) (*RunOutcome, error) {
	sess, err := s.Store.GetSession(ctx, in.SessionID)
	if err != nil {
		return nil, err
	}
	pb, ok := s.Playbooks.Get(in.PlaybookID)
	if !ok {
		return nil, fmt.Errorf("session: unknown playbook %q", in.PlaybookID)
	}

	spec, err := s.Store.LatestSpec(ctx, sess.ID)
	if err == store.ErrNotFound {
		if in.Idea == "" {
			return nil, fmt.Errorf("session: no spec and no idea provided")
		}
		spec, err = s.AddSpec(ctx, sess.ID, in.Idea)
	}
	if err != nil {
		return nil, err
	}

	plan := domain.Plan{
		ID: uuid.NewString(), SessionID: sess.ID, Version: 1,
		Status: domain.PlanActive, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreatePlan(ctx, plan); err != nil {
		return nil, err
	}
	node := domain.PlanNode{
		ID: uuid.NewString(), PlanID: plan.ID, PlaybookID: pb.ID, PlaybookVersion: pb.Version,
		Title: pb.Title, State: domain.NodePending, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreatePlanNode(ctx, node); err != nil {
		return nil, err
	}
	run := domain.PlaybookRun{
		ID: uuid.NewString(), SessionID: sess.ID, PlanNodeID: node.ID,
		PlaybookID: pb.ID, PlaybookVersion: pb.Version, State: domain.NodePending,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreatePlaybookRun(ctx, run); err != nil {
		return nil, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sess.ID, Type: domain.EventPlanCreated,
		Payload: map[string]any{"plan_id": plan.ID, "version": plan.Version, "nodes": 1},
	}); err != nil {
		return nil, err
	}

	_ = s.Store.UpdatePlanNodeState(ctx, node.ID, domain.NodeRunning)
	_ = s.Store.UpdatePlaybookRunState(ctx, run.ID, domain.NodeRunning)

	result, runErr := s.Supervisor.Run(ctx, orchestrator.RunInput{
		SessionID: sess.ID, PlaybookRunID: run.ID, Playbook: pb, Spec: spec.Content,
	})

	outcome := &RunOutcome{
		SessionID: sess.ID, PlanID: plan.ID, PlanNodeID: node.ID,
		PlaybookRunID: run.ID, Result: result,
	}
	if runErr != nil {
		outcome.Status = domain.NodeFailed
		_ = s.Store.UpdatePlaybookRunState(ctx, run.ID, domain.NodeFailed)
		_ = s.Store.UpdatePlanNodeState(ctx, node.ID, domain.NodeFailed)
		return outcome, runErr
	}
	outcome.Status = domain.NodeDone
	_ = s.Store.UpdatePlaybookRunState(ctx, run.ID, domain.NodeDone)
	_ = s.Store.UpdatePlanNodeState(ctx, node.ID, domain.NodeDone)
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sess.ID, Type: domain.EventPlanDone, Payload: map[string]any{"plan_id": plan.ID},
	}); err != nil {
		return outcome, err
	}
	return outcome, nil
}
