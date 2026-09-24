// Package worker runs durable plan jobs with leases so execution is decoupled
// from the API process and survives restarts.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/session"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Worker claims jobs from the store and executes them.
type Worker struct {
	Store   store.Store
	Service *session.Service
	Log     *events.Log
	Logger  *slog.Logger
	// ID identifies this worker for leases.
	ID string
	// Lease is how long a claim is valid without a heartbeat.
	Lease time.Duration
	// HeartbeatInterval overrides the heartbeat/lease-check period (defaults to
	// lease/3, at least one second).
	HeartbeatInterval time.Duration
	// Poll is the idle poll interval.
	Poll time.Duration
	// MaxAttempts bounds retries per job.
	MaxAttempts int
}

// Run processes jobs until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	for {
		did, err := w.RunOnce(ctx)
		if err != nil {
			w.log().Error("job failed", "worker", w.ID, "error", err.Error())
		}
		if did {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// RunOnce claims and executes a single job. It reports whether a job was run.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	lease := w.Lease
	if lease <= 0 {
		lease = 60 * time.Second
	}
	job, err := w.Store.ClaimJob(ctx, w.ID, lease)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	w.emit(ctx, job, domain.EventJobLeased, nil)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := w.startHeartbeat(runCtx, job.ID, lease, cancel)
	runErr := w.execute(runCtx, job)
	stop()

	// A cancelled job is not retried or failed; it is marked cancelled.
	if errors.Is(runErr, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled) {
		w.emit(ctx, job, domain.EventJobFailed, map[string]any{"cancelled": true})
		return true, w.Store.CompleteJob(ctx, job.ID, domain.JobCancelled, "cancelled")
	}

	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}
	if errMsg != "" {
		maxAttempts := job.MaxAttempts
		if w.MaxAttempts > 0 && maxAttempts > w.MaxAttempts {
			maxAttempts = w.MaxAttempts
		}
		if job.Attempts < maxAttempts {
			w.log().Warn("job requeued", "job", job.ID, "kind", job.Kind, "session", job.SessionID,
				"attempt", job.Attempts, "error", errMsg)
			w.emit(ctx, job, domain.EventJobRequeued, map[string]any{"error": errMsg, "attempt": job.Attempts})
			return true, w.Store.CompleteJob(ctx, job.ID, domain.JobQueued, errMsg)
		}
		w.log().Error("job failed", "job", job.ID, "kind", job.Kind, "session", job.SessionID,
			"attempts", job.Attempts, "error", errMsg)
		w.emit(ctx, job, domain.EventJobFailed, map[string]any{"error": errMsg})
		return true, w.Store.CompleteJob(ctx, job.ID, domain.JobFailed, errMsg)
	}
	w.emit(ctx, job, domain.EventJobDone, nil)
	return true, w.Store.CompleteJob(ctx, job.ID, domain.JobDone, "")
}

// execute dispatches a job by kind.
func (w *Worker) execute(ctx context.Context, job *domain.Job) error {
	switch job.Kind {
	case "", domain.JobRunPlan:
		return w.execRunPlan(ctx, job)
	case domain.JobInterview:
		return w.execInterview(ctx, job)
	case domain.JobPlan:
		return w.execPlan(ctx, job)
	case domain.JobSession:
		return w.execSession(ctx, job)
	default:
		return fmt.Errorf("worker: unknown job kind %q", job.Kind)
	}
}

func (w *Worker) execRunPlan(ctx context.Context, job *domain.Job) error {
	if err := w.reconcile(ctx, job.PlanID); err != nil {
		return err
	}
	report, err := w.Service.RunPlan(ctx, job.SessionID, job.PlanID)
	if err != nil {
		return err
	}
	if report != nil && report.Status == "failed" {
		if w.Service.Controller == nil {
			return fmt.Errorf("plan failed")
		}
		if _, err := w.Service.EnqueueSession(ctx, job.SessionID); err != nil {
			return err
		}
		return nil
	}
	// When the plan stops making progress (done), hand control back to the
	// session controller so it can review, replan or finish.
	if report != nil && report.Status == "done" && w.Service.Controller != nil {
		if _, err := w.Service.EnqueueSession(ctx, job.SessionID); err != nil {
			return err
		}
	}
	return nil
}

// execSession runs one controller decision cycle.
func (w *Worker) execSession(ctx context.Context, job *domain.Job) error {
	if w.Service.Controller == nil {
		return fmt.Errorf("worker: session controller is not configured")
	}
	return w.Service.Controller.Step(ctx, job.SessionID)
}

func (w *Worker) execInterview(ctx context.Context, job *domain.Job) error {
	var p struct {
		Mode        string   `json:"mode"`
		Idea        string   `json:"idea"`
		InterviewID string   `json:"interview_id"`
		Selected    []string `json:"selected"`
		Custom      string   `json:"custom"`
	}
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return fmt.Errorf("worker: interview payload: %w", err)
		}
	}
	switch p.Mode {
	case "start":
		_, _, err := w.Service.StartInterview(ctx, job.SessionID, p.Idea)
		return err
	case "resume":
		_, _, err := w.Service.ResumeInterview(ctx, p.InterviewID, p.Selected, p.Custom)
		return err
	default:
		return fmt.Errorf("worker: interview job unknown mode %q", p.Mode)
	}
}

func (w *Worker) execPlan(ctx context.Context, job *domain.Job) error {
	plan, err := w.Service.PlanFromSpec(ctx, job.SessionID)
	if err != nil {
		return err
	}
	_, err = w.Service.EnqueuePlan(ctx, job.SessionID, plan.ID)
	return err
}

// reconcile resets nodes stuck in "running" (e.g. after a crashed worker) so
// the scheduler re-runs them.
func (w *Worker) reconcile(ctx context.Context, planID string) error {
	if planID == "" {
		return nil
	}
	nodes, err := w.Store.ListPlanNodes(ctx, planID)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.State == domain.NodeRunning {
			if err := w.Store.UpdatePlanNodeState(ctx, n.ID, domain.NodeReady); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Worker) startHeartbeat(ctx context.Context, jobID string, lease time.Duration, cancel context.CancelFunc) func() {
	interval := w.HeartbeatInterval
	if interval <= 0 {
		interval = lease / 3
		if interval < time.Second {
			interval = time.Second
		}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.Store.HeartbeatJob(ctx, jobID, w.ID, lease); err != nil {
					w.log().Warn("heartbeat failed", "job", jobID, "error", err.Error())
				}
				if requested, err := w.Store.JobCancelRequested(ctx, jobID); err == nil && requested {
					w.log().Info("job cancel requested", "job", jobID)
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (w *Worker) emit(ctx context.Context, job *domain.Job, typ string, extra map[string]any) {
	if w.Log == nil {
		return
	}
	payload := map[string]any{"job_id": job.ID, "plan_id": job.PlanID, "kind": job.Kind}
	for k, v := range extra {
		payload[k] = v
	}
	_, _ = w.Log.Append(ctx, domain.Event{SessionID: job.SessionID, Type: typ, Payload: payload})
}

func (w *Worker) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
