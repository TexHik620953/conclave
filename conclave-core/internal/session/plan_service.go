package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/scheduler"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

// PlanNodeSpec describes a node when submitting a plan.
type PlanNodeSpec struct {
	ID         string `json:"id"`
	Kind       string `json:"kind,omitempty"` // playbook (default) | gate
	PlaybookID string `json:"playbook_id,omitempty"`
	RoleID     string `json:"role_id,omitempty"`
	Grade      string `json:"grade,omitempty"`
	Title      string `json:"title,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
}

// PlanEdgeSpec describes a dependency between nodes.
type PlanEdgeSpec struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"` // dependency (default) | data | review
}

// CreatePlanInput is a plan submission.
type CreatePlanInput struct {
	SessionID string
	Nodes     []PlanNodeSpec
	Edges     []PlanEdgeSpec
}

// PlanView is the read model of a plan.
type PlanView struct {
	Plan      *domain.Plan      `json:"plan"`
	Nodes     []domain.PlanNode `json:"nodes"`
	Edges     []domain.PlanEdge `json:"edges"`
	Artifacts []domain.Artifact `json:"artifacts"`
	Questions []domain.Question `json:"questions"`
}

// CreatePlan validates and stores a new plan graph (version 1).
func (s *Service) CreatePlan(ctx context.Context, in CreatePlanInput) (*domain.Plan, error) {
	if _, err := s.Store.GetSession(ctx, in.SessionID); err != nil {
		return nil, err
	}
	plan, err := s.createPlanVersion(ctx, in.SessionID, 1, in, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: in.SessionID, Type: domain.EventPlanCreated,
		Payload: map[string]any{"plan_id": plan.ID, "nodes": len(in.Nodes), "edges": len(in.Edges)},
	}); err != nil {
		return nil, err
	}
	return plan, nil
}

// RevisePlan creates a new plan version from a full revised graph, preserving
// the state and output of nodes whose ids are kept from the base plan. The base
// plan is marked revised.
func (s *Service) RevisePlan(ctx context.Context, sessionID, basePlanID string, in CreatePlanInput) (*domain.Plan, error) {
	base, err := s.Store.GetPlan(ctx, basePlanID)
	if err != nil {
		return nil, err
	}
	if base.SessionID != sessionID {
		return nil, fmt.Errorf("session: plan %s does not belong to session %s", basePlanID, sessionID)
	}
	maxVersions := s.MaxPlanVersions
	if maxVersions <= 0 {
		maxVersions = 5
	}
	if base.Version >= maxVersions {
		return nil, fmt.Errorf("session: plan version limit reached (%d)", maxVersions)
	}
	prev := map[string]domain.PlanNode{}
	if nodes, err := s.Store.ListPlanNodes(ctx, basePlanID); err == nil {
		for _, n := range nodes {
			prev[n.Key] = n
		}
	}
	in.SessionID = sessionID
	plan, err := s.createPlanVersion(ctx, sessionID, base.Version+1, in, prev)
	if err != nil {
		return nil, err
	}
	if err := s.Store.UpdatePlanStatus(ctx, basePlanID, domain.PlanRevised); err != nil {
		return nil, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventPlanRevised,
		Payload: map[string]any{"base_plan_id": basePlanID, "plan_id": plan.ID, "version": plan.Version},
	}); err != nil {
		return nil, err
	}
	return plan, nil
}

// createPlanVersion validates a graph and stores it as a new plan version.
// prev, when set, carries node state/output to preserve for kept node ids.
func (s *Service) createPlanVersion(ctx context.Context, sessionID string, version int, in CreatePlanInput, prev map[string]domain.PlanNode) (*domain.Plan, error) {
	if len(in.Nodes) == 0 {
		return nil, fmt.Errorf("session: plan requires at least one node")
	}
	ids := map[string]bool{}
	for _, n := range in.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("session: plan node with empty id")
		}
		if ids[n.ID] {
			return nil, fmt.Errorf("session: duplicate node id %q", n.ID)
		}
		ids[n.ID] = true
		kind := n.Kind
		if kind == "" {
			kind = domain.NodeKindPlaybook
		}
		if kind == domain.NodeKindPlaybook {
			roleKey := n.PlaybookID
			if roleKey == "" {
				roleKey = n.RoleID
			}
			if _, ok := s.Playbooks.Get(roleKey); !ok {
				return nil, fmt.Errorf("session: unknown role %q", roleKey)
			}
		} else if kind != domain.NodeKindGate {
			return nil, fmt.Errorf("session: unknown node kind %q", kind)
		}
	}
	for _, e := range in.Edges {
		if !ids[e.From] || !ids[e.To] {
			return nil, fmt.Errorf("session: edge %s->%s references unknown node", e.From, e.To)
		}
	}

	plan := domain.Plan{
		ID: uuid.NewString(), SessionID: sessionID, Version: version,
		Status: domain.PlanActive, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreatePlan(ctx, plan); err != nil {
		return nil, err
	}
	for _, n := range in.Nodes {
		kind := n.Kind
		if kind == "" {
			kind = domain.NodeKindPlaybook
		}
		pbVersion := 0
		roleKey := n.PlaybookID
		if roleKey == "" {
			roleKey = n.RoleID
		}
		if kind == domain.NodeKindPlaybook {
			if pb, ok := s.Playbooks.Get(roleKey); ok {
				pbVersion = pb.Version
				if n.Title == "" {
					n.Title = pb.Title
				}
			}
		}
		node := domain.PlanNode{
			ID: uuid.NewString(), Key: n.ID, PlanID: plan.ID, Kind: kind, PlaybookID: roleKey,
			RoleID: roleKey, Grade: n.Grade,
			PlaybookVersion: pbVersion, Title: n.Title, Prompt: n.Prompt,
			State: domain.NodePending, CreatedAt: time.Now().UTC(),
		}
		if p, ok := prev[n.ID]; ok {
			// Preserve the progress of a kept node.
			node.State = p.State
			node.Output = p.Output
		}
		if err := s.Store.CreatePlanNode(ctx, node); err != nil {
			return nil, err
		}
	}
	for _, e := range in.Edges {
		kind := domain.EdgeKind(e.Kind)
		if kind == "" {
			kind = domain.EdgeDependency
		}
		if err := s.Store.CreatePlanEdge(ctx, domain.PlanEdge{
			PlanID: plan.ID, From: e.From, To: e.To, Kind: kind,
		}); err != nil {
			return nil, err
		}
	}
	return &plan, nil
}

// RunPlan executes a plan to completion or until it waits for user input.
func (s *Service) RunPlan(ctx context.Context, sessionID, planID string) (*scheduler.RunReport, error) {
	if s.Scheduler == nil {
		return nil, fmt.Errorf("session: scheduler is not configured")
	}
	if _, err := s.Store.GetPlan(ctx, planID); err != nil {
		return nil, err
	}
	return s.Scheduler.Run(ctx, sessionID, planID)
}

// PlanReadModel returns the current graph and related data.
func (s *Service) PlanReadModel(ctx context.Context, sessionID string) (*PlanView, error) {
	plan, err := s.Store.ActivePlan(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	nodes, _ := s.Store.ListPlanNodes(ctx, plan.ID)
	edges, _ := s.Store.ListPlanEdges(ctx, plan.ID)
	arts, _ := s.Store.ListArtifacts(ctx, sessionID)
	questions, _ := s.Store.ListQuestions(ctx, sessionID)
	return &PlanView{Plan: plan, Nodes: nodes, Edges: edges, Artifacts: arts, Questions: questions}, nil
}

// Execute implements scheduler.NodeExecutor for playbook nodes.
func (s *Service) Execute(ctx context.Context, in scheduler.NodeExecInput) (*scheduler.NodeResult, error) {
	ctx = WithSessionID(ctx, in.SessionID)
	ctx = stream.WithSink(ctx, in.SessionID, s.DeltaSink)
	ctx = stream.WithInbox(ctx, s.InboxDrain(in.SessionID))
	pb, ok := s.Playbooks.Get(in.Node.PlaybookID)
	if !ok {
		return nil, fmt.Errorf("session: unknown playbook %q", in.Node.PlaybookID)
	}
	run := domain.PlaybookRun{
		ID: uuid.NewString(), SessionID: in.SessionID, PlanNodeID: in.Node.ID,
		PlaybookID: pb.ID, PlaybookVersion: pb.Version, State: domain.NodeRunning,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreatePlaybookRun(ctx, run); err != nil {
		return nil, err
	}
	spec := in.Spec
	if len(in.Inputs) > 0 {
		spec += "\n\n## Inputs from upstream\n" + strings.Join(in.Inputs, "\n\n")
	}
	result, err := s.Supervisor.Run(ctx, orchestrator.RunInput{
		SessionID: in.SessionID, PlaybookRunID: run.ID, Playbook: pb, Spec: spec,
		PreferredTier: in.Node.Grade,
	})
	if err != nil {
		_ = s.Store.UpdatePlaybookRunState(ctx, run.ID, domain.NodeFailed)
		return nil, err
	}
	_ = s.Store.UpdatePlaybookRunState(ctx, run.ID, domain.NodeDone)
	return &scheduler.NodeResult{Output: result.Output, Artifacts: result.Artifacts}, nil
}

// Open implements scheduler.Gatekeeper: it creates a question for a gate node
// and auto-answers it with the recommended option when the session allows.
func (s *Service) Open(ctx context.Context, sessionID string, node domain.PlanNode) (bool, bool, error) {
	text := node.Prompt
	if text == "" {
		text = node.Title
	}
	if text == "" {
		text = "Approve this step?"
	}
	q := domain.Question{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Kind:      domain.QuestionKindGate,
		Ref:       node.ID, // gate node id
		Text:      text,
		Options: []domain.Option{
			{Label: "Approve", Recommended: true},
			{Label: "Reject"},
		},
		AutoPolicy: domain.AutoInherit,
		State:      domain.QuestionOpen,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.Store.CreateQuestion(ctx, q); err != nil {
		return false, false, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventQuestionAsked,
		Payload: map[string]any{"question_id": q.ID, "node_id": node.ID, "text": text},
	}); err != nil {
		return false, false, err
	}

	sess, err := s.Store.GetSession(ctx, sessionID)
	if err == nil && sess.AutoAccept {
		if err := s.resolveQuestion(ctx, q, []string{"Approve"}, "", true); err != nil {
			return false, false, err
		}
		return true, true, nil
	}
	return false, false, nil
}

// AnswerQuestion records a user answer, resolves the gate and enqueues the plan
// to continue in a background worker.
func (s *Service) AnswerQuestion(ctx context.Context, questionID string, selected []string, custom string) (*domain.Job, error) {
	q, err := s.Store.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if q.State != domain.QuestionOpen {
		return nil, fmt.Errorf("session: question %s is already %s", questionID, q.State)
	}
	if err := s.resolveQuestion(ctx, *q, selected, custom, false); err != nil {
		return nil, err
	}
	node, err := s.Store.GetPlanNode(ctx, q.Ref)
	if err != nil {
		return nil, err
	}
	state := domain.NodeFailed
	if approved(selected, custom) {
		state = domain.NodeDone
	}
	if err := s.Store.UpdatePlanNodeState(ctx, node.ID, state); err != nil {
		return nil, err
	}
	return s.EnqueuePlan(ctx, q.SessionID, node.PlanID)
}

func (s *Service) resolveQuestion(ctx context.Context, q domain.Question, selected []string, custom string, auto bool) error {
	ans := domain.Answer{
		ID: uuid.NewString(), QuestionID: q.ID, Selected: selected, Custom: custom,
		Auto: auto, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.CreateAnswer(ctx, ans); err != nil {
		return err
	}
	if err := s.Store.UpdateQuestionState(ctx, q.ID, domain.QuestionAnswered); err != nil {
		return err
	}
	_, err := s.Log.Append(ctx, domain.Event{
		SessionID: q.SessionID, Type: domain.EventQuestionAnswered,
		Payload: map[string]any{"question_id": q.ID, "selected": selected, "auto": auto},
	})
	return err
}

func approved(selected []string, custom string) bool {
	for _, s := range selected {
		if strings.EqualFold(s, "Approve") {
			return true
		}
	}
	return len(selected) == 0 && strings.TrimSpace(custom) != ""
}

// GetQuestion returns a question by id.
func (s *Service) GetQuestion(ctx context.Context, id string) (*domain.Question, error) {
	return s.Store.GetQuestion(ctx, id)
}
