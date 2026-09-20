package orchestrator

import (
	"sort"
	"strings"
	"sync"

	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/tool"
)

// State is the shared blackboard for a pipeline run.
type State struct {
	mu         sync.RWMutex
	RunID      string
	Task       string
	Workspace  string
	Inputs     map[string]string
	Outputs    map[string]string
	Artifacts  map[string]string
	Usage      provider.Usage
	Iterations int
	CostUSD    float64
	todos      []tool.Todo
	warned     bool
	completed  map[string]bool
}

// NewState creates an empty run state.
func NewState(runID, task, workspace string) *State {
	return &State{
		RunID:     runID,
		Task:      task,
		Workspace: workspace,
		Inputs:    map[string]string{},
		Outputs:   map[string]string{},
		Artifacts: map[string]string{},
		completed: map[string]bool{},
	}
}

// MarkCompleted records a node as completed (used when resuming).
func (s *State) MarkCompleted(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed == nil {
		s.completed = map[string]bool{}
	}
	s.completed[id] = true
}

func (s *State) completedNodes() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.completed))
	for k, v := range s.completed {
		out[k] = v
	}
	return out
}

// Artifact returns an artifact's content.
func (s *State) Artifact(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Artifacts[name]
}

// SetOutput records a node output.
func (s *State) SetOutput(nodeID, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Outputs[nodeID] = content
}

// Output returns a node output.
func (s *State) Output(nodeID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Outputs[nodeID]
}

// SetArtifact records an artifact.
func (s *State) SetArtifact(name, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Artifacts[name] = content
}

// Todos returns a copy of the current todo list.
func (s *State) Todos() []tool.Todo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]tool.Todo, len(s.todos))
	copy(out, s.todos)
	return out
}

// SetTodos replaces the todo list.
func (s *State) SetTodos(todos []tool.Todo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.todos = append([]tool.Todo{}, todos...)
	return nil
}

// SetIterations records the current loop iteration.
func (s *State) SetIterations(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Iterations = i
}

// AddCost accumulates USD cost.
func (s *State) AddCost(usd float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CostUSD += usd
}

// Cost returns the accumulated USD cost.
func (s *State) Cost() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.CostUSD
}

// MarkBudgetWarned returns true only the first time it is called.
func (s *State) MarkBudgetWarned() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.warned {
		return false
	}
	s.warned = true
	return true
}

// AddUsage accumulates token usage.
func (s *State) AddUsage(u provider.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Usage.PromptTokens += u.PromptTokens
	s.Usage.CompletionTokens += u.CompletionTokens
	s.Usage.TotalTokens += u.TotalTokens
}

// Snapshot returns a copy of outputs for safe iteration.
func (s *State) Snapshot() (map[string]string, map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	outs := make(map[string]string, len(s.Outputs))
	for k, v := range s.Outputs {
		outs[k] = v
	}
	arts := make(map[string]string, len(s.Artifacts))
	for k, v := range s.Artifacts {
		arts[k] = v
	}
	return outs, arts
}

// ContextBlock renders the task and prior outputs as prompt context.
func (s *State) ContextBlock() string {
	outs, _ := s.Snapshot()
	var b strings.Builder
	if s.Task != "" {
		b.WriteString("## Task\n")
		b.WriteString(s.Task)
		b.WriteString("\n")
	}
	if len(s.Inputs) > 0 {
		b.WriteString("\n## Inputs\n")
		for _, k := range sortedKeys(s.Inputs) {
			b.WriteString("### " + k + "\n")
			b.WriteString(s.Inputs[k])
			b.WriteString("\n")
		}
	}
	if len(outs) > 0 {
		b.WriteString("\n## Prior outputs\n")
		for _, k := range sortedKeys(outs) {
			b.WriteString("### " + k + "\n")
			b.WriteString(outs[k])
			b.WriteString("\n")
		}
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
