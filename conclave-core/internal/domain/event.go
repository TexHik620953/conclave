package domain

// Event type constants. Events are append-only and form the source of truth for
// plan, session and task projections.
const (
	EventSessionCreated   = "session.created"
	EventSessionRenamed   = "session.renamed"
	EventSessionPaused    = "session.paused"
	EventSessionResumed   = "session.resumed"
	EventSessionDone      = "session.done"
	EventSessionFailed    = "session.failed"
	EventSessionCancelled = "session.cancelled"

	EventSpecCreated = "spec.created"

	EventInterviewStarted  = "interview.started"
	EventInterviewFinished = "interview.finished"

	EventPlanCreated = "plan.created"
	EventPlanRevised = "plan.revised"
	EventPlanDone    = "plan.done"

	EventNodeStateChanged = "node.state_changed"

	EventPlaybookStarted  = "playbook.started"
	EventPlaybookFinished = "playbook.finished"
	EventPlaybookFailed   = "playbook.failed"

	EventTaskCreated  = "task.created"
	EventTaskStarted  = "task.started"
	EventTaskFinished = "task.finished"
	EventTaskFailed   = "task.failed"

	EventToolCall   = "tool.call"
	EventToolResult = "tool.result"

	EventQuestionAsked    = "question.asked"
	EventQuestionAnswered = "question.answered"

	EventArtifactCreated = "artifact.created"
	EventMessageCreated  = "message.created"
	EventUserMessage     = "user.message"
	EventUsageRecorded   = "usage.recorded"

	EventCriticVerdict = "critic.verdict"
	EventError         = "error"

	EventJobQueued   = "job.queued"
	EventJobLeased   = "job.leased"
	EventJobDone     = "job.done"
	EventJobFailed   = "job.failed"
	EventJobRequeued = "job.requeued"

	EventAgentConnected    = "agent.connected"
	EventAgentDisconnected = "agent.disconnected"
)

// NewEvent builds an event with the given type and payload.
func NewEvent(sessionID, typ string, payload map[string]any) Event {
	return Event{SessionID: sessionID, Type: typ, Payload: payload}
}
