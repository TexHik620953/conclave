package orchestrator

import (
	"context"
	"sync"
)

// FakeBrain returns a scripted sequence of decisions, then finishes.
type FakeBrain struct {
	// Decisions are returned in order. When exhausted, Finish is returned.
	Decisions []Decision
	// Finish is the summary returned after Decisions are exhausted.
	Finish string
	// Fn, when set, overrides Decisions entirely.
	Fn func(in DecisionInput) Decision

	mu   sync.Mutex
	seen []DecisionInput
}

// Decide implements Brain.
func (f *FakeBrain) Decide(_ context.Context, in DecisionInput) (Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, in)
	if f.Fn != nil {
		return f.Fn(in), nil
	}
	if len(f.Decisions) > 0 {
		d := f.Decisions[0]
		f.Decisions = f.Decisions[1:]
		return d, nil
	}
	return Decision{Finish: f.Finish}, nil
}

// Seen returns the decision inputs observed so far.
func (f *FakeBrain) Seen() []DecisionInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]DecisionInput, len(f.seen))
	copy(out, f.seen)
	return out
}

// FakeRoleRunner returns canned role output.
type FakeRoleRunner struct {
	// Fn, when set, produces the output for each role input.
	Fn func(in RoleInput) string

	mu    sync.Mutex
	calls []RoleInput
}

// Run implements RoleRunner.
func (f *FakeRoleRunner) Run(_ context.Context, in RoleInput) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	if f.Fn != nil {
		return f.Fn(in), nil
	}
	return "output for: " + in.Task, nil
}

// Calls returns the role inputs observed so far.
func (f *FakeRoleRunner) Calls() []RoleInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RoleInput, len(f.calls))
	copy(out, f.calls)
	return out
}
