package domain

import "fmt"

// terminalNodeStates are states from which a node does not transition further.
var terminalNodeStates = map[NodeState]bool{
	NodeDone: true, NodeFailed: true, NodeCancelled: true,
}

// nodeTransitions defines the allowed state machine for plan nodes and
// playbook runs. Human gates (waiting_user) and dependency waits do not block
// unrelated siblings; that is enforced by the scheduler, not here.
var nodeTransitions = map[NodeState][]NodeState{
	NodePending:           {NodeReady, NodeRunning, NodeCancelled},
	NodeReady:             {NodeRunning, NodeWaitingDependency, NodeCancelled},
	NodeRunning:           {NodeReady, NodeWaitingUser, NodeReview, NodeDone, NodeFailed, NodeCancelled},
	NodeWaitingDependency: {NodeReady, NodeRunning, NodeCancelled},
	NodeWaitingUser:       {NodeReady, NodeRunning, NodeReview, NodeDone, NodeFailed, NodeCancelled},
	NodeReview:            {NodeRunning, NodeDone, NodeFailed, NodeCancelled},
	NodeDone:              {},
	NodeFailed:            {NodeReady, NodeRunning, NodeCancelled},
	NodeCancelled:         {},
}

// CanTransitionNode reports whether a node may move from one state to another.
func CanTransitionNode(from, to NodeState) bool {
	if from == to {
		return true
	}
	for _, s := range nodeTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// TransitionNode validates a node transition, returning a descriptive error.
func TransitionNode(from, to NodeState) error {
	if !CanTransitionNode(from, to) {
		return fmt.Errorf("invalid node transition %s -> %s", from, to)
	}
	return nil
}

// IsTerminalNode reports whether a node state is terminal.
func IsTerminalNode(s NodeState) bool { return terminalNodeStates[s] }

// taskTransitions defines the allowed state machine for tasks.
var taskTransitions = map[TaskState][]TaskState{
	TaskPending:     {TaskRunning, TaskCancelled},
	TaskRunning:     {TaskWaitingUser, TaskReview, TaskDone, TaskFailed, TaskCancelled},
	TaskWaitingUser: {TaskRunning, TaskReview, TaskFailed, TaskCancelled},
	TaskReview:      {TaskRunning, TaskDone, TaskFailed, TaskCancelled},
	TaskDone:        {},
	TaskFailed:      {TaskRunning, TaskCancelled},
	TaskCancelled:   {},
}

// CanTransitionTask reports whether a task may move from one state to another.
func CanTransitionTask(from, to TaskState) bool {
	if from == to {
		return true
	}
	for _, s := range taskTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// IsTerminalTask reports whether a task state is terminal.
func IsTerminalTask(s TaskState) bool {
	return s == TaskDone || s == TaskFailed || s == TaskCancelled
}

// CanTransitionSession reports whether a session may move between states.
func CanTransitionSession(from, to SessionStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case SessionActive:
		return to == SessionPaused || to == SessionDone || to == SessionFailed || to == SessionCancelled
	case SessionPaused:
		return to == SessionActive || to == SessionCancelled || to == SessionFailed
	case SessionDone, SessionFailed, SessionCancelled:
		// Reopening a finished session (e.g. a new user message) is allowed.
		return to == SessionActive
	default:
		return false
	}
}
