package web

import "sync"

// inboxManager holds user messages that arrived while a run was active. The
// orchestrator drains them between steps and injects them into the context.
type inboxManager struct {
	mu      sync.Mutex
	pending map[string][]string
}

func newInboxManager() *inboxManager {
	return &inboxManager{pending: map[string][]string{}}
}

func (m *inboxManager) push(runID, message string) {
	if runID == "" || message == "" {
		return
	}
	m.mu.Lock()
	m.pending[runID] = append(m.pending[runID], message)
	m.mu.Unlock()
}

// drain returns and clears the queued messages for a run.
func (m *inboxManager) drain(runID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.pending[runID]
	delete(m.pending, runID)
	return msgs
}

func (m *inboxManager) clear(runID string) {
	m.mu.Lock()
	delete(m.pending, runID)
	m.mu.Unlock()
}
