package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/obs"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
)

// Supervisor runs a single playbook by repeatedly asking a Brain for the next
// action, running roles and recording events/artifacts. When a Critic is set,
// it reviews the final result and can reopen the work (up to MaxReopens),
// escalating the minimum role tier via the Dispatcher.
type Supervisor struct {
	Brain      Brain
	Roles      RoleRunner
	Artifacts  ArtifactWriter
	Log        *events.Log
	MaxSteps   int
	Critic     Critic
	Dispatcher *Dispatcher
	MaxReopens int
}

// RunInput identifies one playbook execution.
type RunInput struct {
	SessionID     string
	PlaybookRunID string
	Playbook      playbook.Playbook
	Spec          string
	// PreferredTier pins the grade to run when the brain delegates to "auto".
	PreferredTier string
}

// RunResult summarizes a supervisor run.
type RunResult struct {
	Output    string
	Steps     []Step
	Artifacts []string
}

// Run executes the playbook until the Brain finishes or MaxSteps is reached.
func (s *Supervisor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	if s.Brain == nil || s.Roles == nil {
		return nil, fmt.Errorf("supervisor: brain and roles are required")
	}
	ctx, span := obs.Span(ctx, "playbook.run",
		attribute.String("playbook", in.Playbook.ID), attribute.String("run_id", in.PlaybookRunID))
	defer span.End()

	maxSteps := s.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 16
	}
	if err := s.emit(ctx, in.SessionID, domain.EventPlaybookStarted, map[string]any{
		"playbook": in.Playbook.ID, "version": in.Playbook.Version, "run_id": in.PlaybookRunID,
	}); err != nil {
		return nil, err
	}

	result := &RunResult{}
	reopens := 0
	minTier := ""
	for step := 0; step < maxSteps; step++ {
		decision, err := s.Brain.Decide(ctx, DecisionInput{
			Playbook: in.Playbook,
			Spec:     in.Spec,
			History:  result.Steps,
		})
		if err != nil {
			s.fail(ctx, in, err)
			return nil, err
		}
		if decision.Delegate == nil {
			if s.Critic != nil && len(result.Steps) > 0 && reopens < s.maxReopens() {
				verdict, err := s.Critic.Review(ctx, CriticInput{
					Spec: in.Spec, Playbook: in.Playbook, Summary: decision.Finish, Steps: result.Steps,
				})
				if err != nil {
					_ = s.emit(ctx, in.SessionID, domain.EventError, map[string]any{"critic_error": err.Error()})
				} else {
					_ = s.emit(ctx, in.SessionID, domain.EventCriticVerdict, map[string]any{
						"pass": verdict.Pass, "feedback": verdict.Feedback,
					})
					obs.Default().Counter("conclave_critic_verdicts_total", "Critic verdicts", obs.Labels{
						"pass": fmt.Sprintf("%t", verdict.Pass),
					}).Inc()
					if !verdict.Pass {
						reopens++
						result.Steps = append(result.Steps, Step{Role: "critic", Task: "review", Output: verdict.Feedback})
						if verdict.SuggestedTier != "" && s.Dispatcher != nil {
							minTier = s.Dispatcher.AtLeast(minTier, verdict.SuggestedTier)
						}
						continue
					}
				}
			}
			return s.finish(ctx, in, decision.Finish, result)
		}

		tier := s.resolveTier(in.Playbook, pinTier(decision.Delegate.Role, in.PreferredTier), minTier)
		role, ok := roleByTier(in.Playbook, tier)
		if !ok {
			err := fmt.Errorf("supervisor: unknown role tier %q", tier)
			s.fail(ctx, in, err)
			return nil, err
		}
		taskID := uuid.NewString()
		if err := s.emit(ctx, in.SessionID, domain.EventTaskCreated, map[string]any{
			"task_id": taskID, "run_id": in.PlaybookRunID, "tier": role.Tier, "task": decision.Delegate.Task,
		}); err != nil {
			return nil, err
		}
		if err := s.emit(ctx, in.SessionID, domain.EventTaskStarted, map[string]any{"task_id": taskID, "tier": role.Tier}); err != nil {
			return nil, err
		}
		output, err := s.Roles.Run(ctx, RoleInput{Role: role, System: in.Playbook.Guidelines, Task: decision.Delegate.Task})
		if err != nil {
			obs.Default().Counter("conclave_role_runs_total", "Role runs by tier and status", obs.Labels{"tier": role.Tier, "status": "failed"}).Inc()
			_ = s.emit(ctx, in.SessionID, domain.EventTaskFailed, map[string]any{"task_id": taskID, "error": err.Error()})
			s.fail(ctx, in, err)
			return nil, err
		}
		obs.Default().Counter("conclave_role_runs_total", "Role runs by tier and status", obs.Labels{"tier": role.Tier, "status": "done"}).Inc()
		if err := s.emit(ctx, in.SessionID, domain.EventTaskFinished, map[string]any{
			"task_id": taskID, "tier": role.Tier, "output": output,
		}); err != nil {
			return nil, err
		}
		result.Steps = append(result.Steps, Step{Role: role.Tier, Task: decision.Delegate.Task, Output: output})
	}

	err := fmt.Errorf("supervisor: reached max steps (%d) without finishing", maxSteps)
	s.fail(ctx, in, err)
	return nil, err
}

func (s *Supervisor) finish(ctx context.Context, in RunInput, summary string, result *RunResult) (*RunResult, error) {
	name := "result.md"
	if len(in.Playbook.Outputs) > 0 {
		name = in.Playbook.Outputs[0]
	}
	result.Output = summary
	if s.Artifacts != nil && summary != "" {
		ref, err := s.Artifacts.Write(ctx, in.SessionID, in.PlaybookRunID, name, summary)
		if err != nil {
			return nil, err
		}
		result.Artifacts = append(result.Artifacts, name)
		if err := s.emit(ctx, in.SessionID, domain.EventArtifactCreated, map[string]any{
			"name": name, "ref": ref, "run_id": in.PlaybookRunID,
		}); err != nil {
			return nil, err
		}
	}
	if err := s.emit(ctx, in.SessionID, domain.EventPlaybookFinished, map[string]any{
		"run_id": in.PlaybookRunID, "steps": len(result.Steps),
	}); err != nil {
		return nil, err
	}
	obs.Default().Counter("conclave_playbook_runs_total", "Playbook runs by status", obs.Labels{
		"playbook": in.Playbook.ID, "status": "done",
	}).Inc()
	return result, nil
}

func (s *Supervisor) fail(ctx context.Context, in RunInput, runErr error) {
	obs.Default().Counter("conclave_playbook_runs_total", "Playbook runs by status", obs.Labels{
		"playbook": in.Playbook.ID, "status": "failed",
	}).Inc()
	_ = s.emit(ctx, in.SessionID, domain.EventPlaybookFailed, map[string]any{
		"run_id": in.PlaybookRunID, "error": runErr.Error(),
	})
}

func (s *Supervisor) emit(ctx context.Context, sessionID, typ string, payload map[string]any) error {
	if s.Log == nil {
		return nil
	}
	_, err := s.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: typ, Payload: payload, CreatedAt: time.Now().UTC(),
	})
	return err
}

func (s *Supervisor) maxReopens() int {
	if s.MaxReopens > 0 {
		return s.MaxReopens
	}
	return 2
}

// resolveTier turns "auto"/empty into the weakest playbook tier and enforces
// the escalation floor set by the critic.
func (s *Supervisor) resolveTier(pb playbook.Playbook, requested, minTier string) string {
	tier := requested
	if tier == "" || tier == "auto" {
		tier = s.lowestTier(pb)
	}
	if minTier != "" && s.Dispatcher != nil && s.Dispatcher.Rank(tier) < s.Dispatcher.Rank(minTier) {
		tier = minTier
	}
	return tier
}

func (s *Supervisor) lowestTier(pb playbook.Playbook) string {
	if len(pb.Roles) == 0 {
		return ""
	}
	if s.Dispatcher == nil {
		return pb.Roles[0].Tier
	}
	best := ""
	bestRank := 1 << 30
	for _, r := range pb.Roles {
		rank := s.Dispatcher.Rank(r.Tier)
		if rank == 0 {
			rank = 1 << 29
		}
		if rank < bestRank {
			bestRank = rank
			best = r.Tier
		}
	}
	return best
}

func roleByTier(p playbook.Playbook, tier string) (playbook.Role, bool) {
	for _, r := range p.Roles {
		if r.Tier == tier {
			return r, true
		}
	}
	return playbook.Role{}, false
}

// pinTier applies a preferred grade when the brain asked for "auto".
func pinTier(requested, preferred string) string {
	if (requested == "" || requested == "auto") && preferred != "" {
		return preferred
	}
	return requested
}
