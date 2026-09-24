package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Planner builds a plan graph from a session's spec using an LLM.
type Planner struct {
	Store     store.Store
	Log       *events.Log
	Playbooks playbook.Provider
	Service   *Service
	Client    llmgw.Client
	Model     string
	// Models resolves the planner model from live config.
	Models llmgw.ModelResolver
}

func (p *Planner) model() string {
	if p.Models != nil {
		if m := p.Models.BrainModel(domain.BrainPlanner); m != "" {
			return m
		}
	}
	return p.Model
}

// Build creates and stores a plan for the session's latest spec.
func (p *Planner) Build(ctx context.Context, sessionID string) (*domain.Plan, error) {
	if p.Client == nil || p.Service == nil {
		return nil, fmt.Errorf("planner: client and service are required")
	}
	ctx = WithSessionID(ctx, sessionID)
	spec, err := p.Store.LatestSpec(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("planner: no spec for session: %w", err)
	}
	resp, err := p.Client.Complete(ctx, llmgw.Request{
		Model:    p.model(),
		System:   p.systemPrompt(),
		Messages: []llmgw.Message{{Role: "user", Content: spec.Content}},
		Tools:    plannerTools(),
	})
	if err != nil {
		return nil, err
	}
	in, err := parsePlan(resp.Message)
	if err != nil {
		return nil, err
	}
	in.SessionID = sessionID
	return p.Service.CreatePlan(ctx, in)
}

// Replan revises an existing plan based on a reason (critic feedback, failure,
// a user message) and stores the result as a new plan version.
func (p *Planner) Replan(ctx context.Context, sessionID, planID, reason string) (*domain.Plan, error) {
	if p.Client == nil || p.Service == nil {
		return nil, fmt.Errorf("planner: client and service are required")
	}
	ctx = WithSessionID(ctx, sessionID)
	specContent := ""
	if spec, err := p.Store.LatestSpec(ctx, sessionID); err == nil {
		specContent = spec.Content
	}
	nodes, err := p.Store.ListPlanNodes(ctx, planID)
	if err != nil {
		return nil, err
	}
	edges, err := p.Store.ListPlanEdges(ctx, planID)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Complete(ctx, llmgw.Request{
		Model:    p.model(),
		System:   p.replanSystemPrompt(),
		Messages: []llmgw.Message{{Role: "user", Content: renderGraph(specContent, nodes, edges, reason)}},
		Tools:    plannerTools(),
	})
	if err != nil {
		return nil, err
	}
	in, err := parsePlan(resp.Message)
	if err != nil {
		return nil, err
	}
	return p.Service.RevisePlan(ctx, sessionID, planID, in)
}

func (p *Planner) replanSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString("You are revising an existing execution plan. Produce the full revised graph with `set_plan`.\n")
	sb.WriteString("Keep the `id` of any node that already ran (done/waiting) so its result is preserved; you may add, remove or rewire other nodes.\n")
	sb.WriteString("Address the reason for the revision. Edge kinds: dependency, data, review.\n\n")
	sb.WriteString("Available playbooks:\n")
	for _, pb := range p.Playbooks.List() {
		fmt.Fprintf(&sb, "- %s — %s\n", pb.ID, pb.Title)
	}
	return sb.String()
}

func renderGraph(spec string, nodes []domain.PlanNode, edges []domain.PlanEdge, reason string) string {
	var sb strings.Builder
	sb.WriteString("## Spec\n")
	sb.WriteString(spec)
	sb.WriteString("\n\n## Current plan\n")
	for _, n := range nodes {
		fmt.Fprintf(&sb, "- node %s [%s] playbook=%s state=%s", n.ID, n.Kind, n.PlaybookID, n.State)
		if n.Title != "" {
			fmt.Fprintf(&sb, " title=%q", n.Title)
		}
		sb.WriteString("\n")
	}
	for _, e := range edges {
		fmt.Fprintf(&sb, "- edge %s -> %s (%s)\n", e.From, e.To, e.Kind)
	}
	if reason != "" {
		sb.WriteString("\n## Reason for revision\n")
		sb.WriteString(reason)
	}
	return sb.String()
}

func (p *Planner) systemPrompt() string {
	var sb strings.Builder
	sb.WriteString("You are the planning controller. Given a spec, produce an execution plan as a graph of playbooks.\n")
	sb.WriteString("Use the `set_plan` tool. Nodes reference a playbook_id and an `id` you choose; edges connect node ids.\n")
	sb.WriteString("Edge kinds: dependency (wait for completion), data (also pass the predecessor's output as input), review (human approval).\n")
	sb.WriteString("Add a gate node (kind: gate) where the user must approve a milestone. Keep the graph as small as needed.\n\n")
	sb.WriteString("Available playbooks:\n")
	for _, pb := range p.Playbooks.List() {
		fmt.Fprintf(&sb, "- %s — %s", pb.ID, pb.Title)
		if len(pb.Inputs) > 0 {
			fmt.Fprintf(&sb, " (inputs: %s)", strings.Join(pb.Inputs, ", "))
		}
		if len(pb.Outputs) > 0 {
			fmt.Fprintf(&sb, " (outputs: %s)", strings.Join(pb.Outputs, ", "))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func plannerTools() []llmgw.ToolDef {
	node := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "description": "Short stable slug (letters/digits), e.g. 'ba', 'arch', 'api'. Not a UUID."},
			"kind":        map[string]any{"type": "string", "enum": []string{"playbook", "gate"}},
			"playbook_id": map[string]any{"type": "string"},
			"role_id":     map[string]any{"type": "string", "description": "Role key (same as playbook_id)."},
			"grade":       map[string]any{"type": "string", "description": "Grade of the role, e.g. junior/middle/senior."},
			"title":       map[string]any{"type": "string"},
			"prompt":      map[string]any{"type": "string"},
		},
		"required": []string{"id"},
	}
	edge := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from": map[string]any{"type": "string"},
			"to":   map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string", "enum": []string{"dependency", "data", "review"}},
		},
		"required": []string{"from", "to"},
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"nodes": map[string]any{"type": "array", "items": node},
			"edges": map[string]any{"type": "array", "items": edge},
		},
		"required": []string{"nodes"},
	}
	return []llmgw.ToolDef{{Name: "set_plan", Description: "Set the execution plan graph.", Parameters: mustJSON(schema)}}
}

func parsePlan(msg llmgw.Message) (CreatePlanInput, error) {
	for _, tc := range msg.ToolCalls {
		if tc.Name != "set_plan" {
			continue
		}
		var raw struct {
			Nodes []struct {
				ID         string `json:"id"`
				Kind       string `json:"kind"`
				PlaybookID string `json:"playbook_id"`
				RoleID     string `json:"role_id"`
				Grade      string `json:"grade"`
				Title      string `json:"title"`
				Prompt     string `json:"prompt"`
			} `json:"nodes"`
			Edges []struct {
				From string `json:"from"`
				To   string `json:"to"`
				Kind string `json:"kind"`
			} `json:"edges"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &raw); err != nil {
			return CreatePlanInput{}, fmt.Errorf("planner: set_plan arguments: %w", err)
		}
		in := CreatePlanInput{}
		for _, n := range raw.Nodes {
			role := n.PlaybookID
			if role == "" {
				role = n.RoleID
			}
			in.Nodes = append(in.Nodes, PlanNodeSpec{
				ID: n.ID, Kind: n.Kind, PlaybookID: role, Grade: n.Grade, Title: n.Title, Prompt: n.Prompt,
			})
		}
		for _, e := range raw.Edges {
			in.Edges = append(in.Edges, PlanEdgeSpec{From: e.From, To: e.To, Kind: e.Kind})
		}
		return in, nil
	}
	return CreatePlanInput{}, fmt.Errorf("planner: model did not return a plan")
}
