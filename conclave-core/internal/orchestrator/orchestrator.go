// Package orchestrator contains the controller hierarchy that plans and runs
// playbooks. P0 implements the per-playbook supervisor; interviewer, planner,
// dispatcher and critic arrive in later phases.
package orchestrator

import (
	"context"

	"github.com/texhik/conclave/conclave-core/internal/playbook"
)

// Step is one delegated task and its result.
type Step struct {
	Role   string `json:"role"`
	Task   string `json:"task"`
	Output string `json:"output"`
}

// DecisionInput is what a Brain sees when deciding the next action.
type DecisionInput struct {
	Playbook playbook.Playbook
	Spec     string
	History  []Step
}

// Delegation is a request to run a role on a task.
type Delegation struct {
	Role string
	Task string
}

// Decision is the supervisor's next action: either delegate or finish.
type Decision struct {
	Delegate *Delegation
	Finish   string
}

// Brain decides the next supervisor action (usually backed by an LLM).
type Brain interface {
	Decide(ctx context.Context, in DecisionInput) (Decision, error)
}

// RoleInput is a request to run a role.
type RoleInput struct {
	Role   playbook.Role
	System string
	Task   string
}

// RoleRunner executes a role on a task and returns its textual output.
type RoleRunner interface {
	Run(ctx context.Context, in RoleInput) (string, error)
}

// ArtifactWriter persists a named artifact and returns a reference.
type ArtifactWriter interface {
	Write(ctx context.Context, sessionID, playbookRunID, name, content string) (string, error)
}
