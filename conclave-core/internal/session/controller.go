package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/scheduler"
)

// StartInterview begins a requirements interview for a session.
func (s *Service) StartInterview(ctx context.Context, sessionID, idea string) (*domain.Interview, *domain.Question, error) {
	if s.Interviewer == nil {
		return nil, nil, fmt.Errorf("session: interviewer is not configured")
	}
	if _, err := s.Store.GetSession(ctx, sessionID); err != nil {
		return nil, nil, err
	}
	return s.Interviewer.Start(ctx, sessionID, idea)
}

// ResumeInterview continues an interview after the user answers.
func (s *Service) ResumeInterview(ctx context.Context, interviewID string, selected []string, custom string) (*domain.Interview, *domain.Question, error) {
	if s.Interviewer == nil {
		return nil, nil, fmt.Errorf("session: interviewer is not configured")
	}
	return s.Interviewer.Resume(ctx, interviewID, selected, custom)
}

// PlanFromSpec builds a plan from the session's spec using the LLM planner.
func (s *Service) PlanFromSpec(ctx context.Context, sessionID string) (*domain.Plan, error) {
	if s.Planner == nil {
		return nil, fmt.Errorf("session: planner is not configured")
	}
	return s.Planner.Build(ctx, sessionID)
}

// Replan revises a plan and returns the new version.
func (s *Service) Replan(ctx context.Context, sessionID, planID, reason string) (*domain.Plan, error) {
	if s.Planner == nil {
		return nil, fmt.Errorf("session: planner is not configured")
	}
	return s.Planner.Replan(ctx, sessionID, planID, reason)
}

// EnqueuePlan schedules a plan for background execution.
func (s *Service) EnqueuePlan(ctx context.Context, sessionID, planID string) (*domain.Job, error) {
	if _, err := s.Store.GetPlan(ctx, planID); err != nil {
		return nil, err
	}
	job := domain.Job{
		ID: uuid.NewString(), Kind: domain.JobRunPlan, SessionID: sessionID, PlanID: planID,
		State: domain.JobQueued, MaxAttempts: 3, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.EnqueueJob(ctx, job); err != nil {
		return nil, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventJobQueued,
		Payload: map[string]any{"job_id": job.ID, "plan_id": planID},
	}); err != nil {
		return nil, err
	}
	return &job, nil
}

// TimelineView is the combined read model for the session timeline.
type TimelineView struct {
	Session   *domain.Session   `json:"session"`
	Events    []domain.Event    `json:"events"`
	Plan      *PlanView         `json:"plan,omitempty"`
	Artifacts []domain.Artifact `json:"artifacts"`
	Questions []domain.Question `json:"questions"`
}

// Timeline returns the session's event log and current plan projection.
func (s *Service) Timeline(ctx context.Context, sessionID string, fromSeq int64) (*TimelineView, error) {
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	events, err := s.Store.ListEvents(ctx, sessionID, fromSeq)
	if err != nil {
		return nil, err
	}
	arts, _ := s.Store.ListArtifacts(ctx, sessionID)
	questions, _ := s.Store.ListQuestions(ctx, sessionID)
	view := &TimelineView{Session: sess, Events: events, Artifacts: arts, Questions: questions}
	if plan, err := s.PlanReadModel(ctx, sessionID); err == nil {
		view.Plan = plan
	}
	return view, nil
}

// enqueueJob stores a job and emits a job.queued event.
func (s *Service) enqueueJob(ctx context.Context, job domain.Job) (*domain.Job, error) {
	if err := s.Store.EnqueueJob(ctx, job); err != nil {
		return nil, err
	}
	payload := map[string]any{"job_id": job.ID, "kind": job.Kind}
	if job.PlanID != "" {
		payload["plan_id"] = job.PlanID
	}
	if _, err := s.Log.Append(ctx, domain.Event{SessionID: job.SessionID, Type: domain.EventJobQueued, Payload: payload}); err != nil {
		return nil, err
	}
	return &job, nil
}

// EnqueueInterview schedules a requirements interview.
func (s *Service) EnqueueInterview(ctx context.Context, sessionID, idea string) (*domain.Job, error) {
	payload, _ := json.Marshal(map[string]any{"mode": "start", "idea": idea})
	return s.enqueueJob(ctx, domain.Job{
		ID: uuid.NewString(), Kind: domain.JobInterview, SessionID: sessionID,
		State: domain.JobQueued, MaxAttempts: 3, Payload: payload, CreatedAt: time.Now().UTC(),
	})
}

// EnqueueInterviewAnswer schedules the continuation of an interview.
func (s *Service) EnqueueInterviewAnswer(ctx context.Context, sessionID, interviewID string, selected []string, custom string) (*domain.Job, error) {
	payload, _ := json.Marshal(map[string]any{
		"mode": "resume", "interview_id": interviewID, "selected": selected, "custom": custom,
	})
	return s.enqueueJob(ctx, domain.Job{
		ID: uuid.NewString(), Kind: domain.JobInterview, SessionID: sessionID,
		State: domain.JobQueued, MaxAttempts: 3, Payload: payload, CreatedAt: time.Now().UTC(),
	})
}

// EnqueuePlanBuild schedules the LLM planner to build a plan from the spec.
func (s *Service) EnqueuePlanBuild(ctx context.Context, sessionID string) (*domain.Job, error) {
	return s.enqueueJob(ctx, domain.Job{
		ID: uuid.NewString(), Kind: domain.JobPlan, SessionID: sessionID,
		State: domain.JobQueued, MaxAttempts: 3, CreatedAt: time.Now().UTC(),
	})
}

// EnqueueSession schedules the session controller to take its next decision.
func (s *Service) EnqueueSession(ctx context.Context, sessionID string) (*domain.Job, error) {
	return s.enqueueJob(ctx, domain.Job{
		ID: uuid.NewString(), Kind: domain.JobSession, SessionID: sessionID,
		State: domain.JobQueued, MaxAttempts: 3, CreatedAt: time.Now().UTC(),
	})
}

// Goal is the single entry point for a user message. The message is recorded as
// a pending in-flight message; when the session is idle the controller is
// enqueued, otherwise the message is injected into the running work. It reports
// whether the message was injected into an already-running session.
func (s *Service) Goal(ctx context.Context, sessionID, content string) (*domain.Job, bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false, fmt.Errorf("session: message is required")
	}
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, false, err
	}
	switch sess.Status {
	case domain.SessionDone, domain.SessionCancelled, domain.SessionFailed:
		if err := s.Store.UpdateSessionStatus(ctx, sessionID, domain.SessionActive); err != nil {
			return nil, false, err
		}
	}
	msg := domain.Message{
		ID: uuid.NewString(), SessionID: sessionID, Kind: domain.MessageKindUser,
		Content: content, State: domain.MessageStatePending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Store.UpsertMessage(ctx, msg); err != nil {
		return nil, false, err
	}
	if _, err := s.Log.Append(ctx, domain.Event{
		SessionID: sessionID, Type: domain.EventUserMessage,
		Payload: map[string]any{"message_id": msg.ID, "content": content},
	}); err != nil {
		return nil, false, err
	}

	if s.busy(ctx, sessionID) {
		return nil, true, nil
	}
	job, err := s.EnqueueSession(ctx, sessionID)
	return job, false, err
}

// busy reports whether the session has queued/leased jobs or an open question.
func (s *Service) busy(ctx context.Context, sessionID string) bool {
	jobs, err := s.Store.ListJobs(ctx, sessionID, 100)
	if err == nil {
		for _, j := range jobs {
			if j.State == domain.JobQueued || j.State == domain.JobLeased {
				return true
			}
		}
	}
	questions, err := s.Store.ListQuestions(ctx, sessionID)
	if err == nil {
		for _, q := range questions {
			if q.State == domain.QuestionOpen {
				return true
			}
		}
	}
	return false
}

// InboxDrain returns a function that consumes pending user messages for a
// session, used to inject messages into running role executions.
func (s *Service) InboxDrain(sessionID string) func(context.Context) []string {
	return func(ctx context.Context) []string {
		msgs, err := s.Store.PendingMessages(ctx, sessionID)
		if err != nil || len(msgs) == 0 {
			return nil
		}
		ids := make([]string, 0, len(msgs))
		out := make([]string, 0, len(msgs))
		for _, m := range msgs {
			ids = append(ids, m.ID)
			out = append(out, m.Content)
		}
		if err := s.Store.MarkMessagesInjected(ctx, ids); err != nil {
			return nil
		}
		return out
	}
}

// QuestionAnswer is one response in a questionnaire submission.
type QuestionAnswer struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected"`
	Custom     string   `json:"custom"`
}

// AnswerQuestions resolves a batch of controller questions and resumes the
// controller only once every open question has been answered. This avoids
// enqueuing one job per answer.
func (s *Service) AnswerQuestions(ctx context.Context, sessionID string, answers []QuestionAnswer) (*domain.Job, error) {
	answered := 0
	for _, a := range answers {
		q, err := s.Store.GetQuestion(ctx, a.QuestionID)
		if err != nil {
			continue
		}
		if q.SessionID != sessionID || q.Kind != domain.QuestionKindController || q.State != domain.QuestionOpen {
			continue
		}
		if err := s.resolveQuestion(ctx, *q, a.Selected, a.Custom, false); err != nil {
			return nil, err
		}
		answered++
	}
	if answered == 0 {
		return nil, nil
	}
	// Wait until all questions are answered before resuming the controller.
	questions, err := s.Store.ListQuestions(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, q := range questions {
		if q.State == domain.QuestionOpen {
			return nil, nil
		}
	}
	return s.EnqueueSession(ctx, sessionID)
}

// AnswerControllerQuestion resolves a single controller question and resumes the
// controller when no questions remain open.
func (s *Service) AnswerControllerQuestion(ctx context.Context, questionID string, selected []string, custom string) (*domain.Job, error) {
	q, err := s.Store.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if q.State != domain.QuestionOpen {
		return nil, fmt.Errorf("session: question %s is already %s", questionID, q.State)
	}
	return s.AnswerQuestions(ctx, q.SessionID, []QuestionAnswer{{QuestionID: questionID, Selected: selected, Custom: custom}})
}

// DeleteSession removes a session and all of its related data.
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	if _, err := s.Store.GetSession(ctx, sessionID); err != nil {
		return err
	}
	return s.Store.DeleteSession(ctx, sessionID)
}

// CancelSession cancels all active jobs and marks the session cancelled.
func (s *Service) CancelSession(ctx context.Context, sessionID string) (int, error) {
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	n, err := s.InterruptSession(ctx, sessionID)
	if err != nil {
		return n, err
	}
	switch sess.Status {
	case domain.SessionActive, domain.SessionPaused:
		if err := s.Store.UpdateSessionStatus(ctx, sessionID, domain.SessionCancelled); err != nil {
			return n, err
		}
	}
	_, err = s.Log.Append(ctx, domain.Event{SessionID: sessionID, Type: domain.EventSessionCancelled})
	return n, err
}

// InterruptSession requests cancellation of the session's active jobs. It
// returns the number of jobs signalled.
func (s *Service) InterruptSession(ctx context.Context, sessionID string) (int, error) {
	jobs, err := s.Store.ListJobs(ctx, sessionID, 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range jobs {
		switch j.State {
		case domain.JobQueued:
			// Cancel queued jobs immediately so no worker starts them.
			if err := s.Store.CompleteJob(ctx, j.ID, domain.JobCancelled, "cancelled"); err != nil {
				return n, err
			}
			n++
		case domain.JobLeased:
			// A running job stops on its next heartbeat.
			if err := s.Store.RequestJobCancel(ctx, j.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// PauseSession marks a session paused.
func (s *Service) PauseSession(ctx context.Context, sessionID string) error {
	if err := s.Store.UpdateSessionStatus(ctx, sessionID, domain.SessionPaused); err != nil {
		return err
	}
	_, err := s.Log.Append(ctx, domain.Event{SessionID: sessionID, Type: domain.EventSessionPaused})
	return err
}

// ResumeSession marks a session active and resumes its active plan.
func (s *Service) ResumeSession(ctx context.Context, sessionID string) (*scheduler.RunReport, error) {
	sess, err := s.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status == domain.SessionPaused {
		if err := s.Store.UpdateSessionStatus(ctx, sessionID, domain.SessionActive); err != nil {
			return nil, err
		}
	}
	if _, err := s.Log.Append(ctx, domain.Event{SessionID: sessionID, Type: domain.EventSessionResumed}); err != nil {
		return nil, err
	}
	plan, err := s.Store.ActivePlan(ctx, sessionID)
	if err != nil {
		return nil, nil // no plan to resume
	}
	return s.RunPlan(ctx, sessionID, plan.ID)
}
