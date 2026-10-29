package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/tools"
)

type Status string

const (
	Running   Status = "running"
	Completed Status = "completed"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

type Job struct {
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	Status     Status    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	output     *ring
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	cmd        *exec.Cmd
}

type ring struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	order   []string
	bus     *event.Bus
	session string
}

func New(bus *event.Bus, sessionID string) *Manager {
	return &Manager{jobs: map[string]*Job{}, bus: bus, session: sessionID}
}

func (m *Manager) Start(cwd, command string) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		ID: domain.NewID("job"), Command: command, Cwd: cwd, Status: Running,
		StartedAt: time.Now(), output: &ring{max: 512 * 1024}, cancel: cancel, done: make(chan struct{}),
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", command)
	j.cmd = cmd
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = j.output
	cmd.Stderr = j.output
	if err := cmd.Start(); err != nil {
		j.Status = Failed
		j.FinishedAt = time.Now()
		j.output.Write([]byte(err.Error()))
		close(j.done)
	} else {
		go func() {
			err := cmd.Wait()
			j.mu.Lock()
			if j.Status == Cancelled {
				j.Status = Cancelled
			} else if err != nil {
				j.Status = Failed
				if ee, ok := err.(*exec.ExitError); ok {
					j.ExitCode = ee.ExitCode()
				}
				j.output.Write([]byte(err.Error()))
			} else {
				j.Status = Completed
			}
			j.FinishedAt = time.Now()
			j.mu.Unlock()
			close(j.done)
			m.publish(j)
		}()
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()
	return j
}

func (m *Manager) publish(j *Job) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(domain.Event{
		Time: time.Now(), SessionID: m.session, Type: "job.finished",
		Text:    fmt.Sprintf("%s: %s", j.ID, j.Status),
		Payload: map[string]any{"id": j.ID, "command": j.Command, "status": j.Status, "exit_code": j.ExitCode},
	})
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.order))
	for _, id := range m.order {
		if j := m.jobs[id]; j != nil {
			out = append(out, j)
		}
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].StartedAt.After(out[k].StartedAt) })
	return out
}

func (m *Manager) Cancel(id string) error {
	j, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	j.mu.Lock()
	if j.Status != Running {
		j.mu.Unlock()
		return fmt.Errorf("job %s is %s", id, j.Status)
	}
	j.Status = Cancelled
	if j.cmd != nil && j.cmd.Process != nil {
		_ = syscall.Kill(-j.cmd.Process.Pid, syscall.SIGKILL)
	}
	j.cancel()
	j.mu.Unlock()
	return nil
}

func (m *Manager) Close() {
	for _, j := range m.List() {
		j.mu.Lock()
		if j.Status == Running {
			j.Status = Cancelled
			if j.cmd != nil && j.cmd.Process != nil {
				_ = syscall.Kill(-j.cmd.Process.Pid, syscall.SIGKILL)
			}
			j.cancel()
		}
		j.mu.Unlock()
	}
}

func (m *Manager) AddTo(r *tools.Registry) {
	r.Add(&startTool{mgr: m})
	r.Add(&listTool{mgr: m})
	r.Add(&outputTool{mgr: m})
	r.Add(&cancelTool{mgr: m})
}

type startTool struct{ mgr *Manager }

func (t *startTool) Name() string { return "shell_background" }
func (t *startTool) Spec() llm.Tool {
	return llm.Tool{Name: "shell_background", Description: "Start a long-running shell command in the background. Returns a job ID. Use job_output to read progress and job_cancel to stop it.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"command": map[string]any{"type": "string"},
		}, "required": []string{"command"}}}
}
func (t *startTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	j := t.mgr.Start(env.Cwd, in.Command)
	return fmt.Sprintf("started %s (%s). Use job_output to follow it.", j.ID, j.Command), nil
}

type listTool struct{ mgr *Manager }

func (t *listTool) Name() string { return "job_list" }
func (t *listTool) Spec() llm.Tool {
	return llm.Tool{Name: "job_list", Description: "List background jobs and their status.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
}
func (t *listTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var b strings.Builder
	for _, j := range t.mgr.List() {
		fmt.Fprintf(&b, "%s\t%s\texit=%d\t%s\n", j.ID, j.Status, j.ExitCode, j.Command)
	}
	if b.Len() == 0 {
		return "no background jobs", nil
	}
	return b.String(), nil
}

type outputTool struct{ mgr *Manager }

func (t *outputTool) Name() string { return "job_output" }
func (t *outputTool) Spec() llm.Tool {
	return llm.Tool{Name: "job_output", Description: "Read the tail of a background job's output.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer", "description": "Max bytes, default 20000"},
		}, "required": []string{"id"}}}
}
func (t *outputTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in struct {
		ID    string `json:"id"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	j, ok := t.mgr.Get(in.ID)
	if !ok {
		return "", fmt.Errorf("job %s not found", in.ID)
	}
	if in.Limit <= 0 {
		in.Limit = 20000
	}
	out := j.output.String()
	if len(out) > in.Limit {
		out = out[len(out)-in.Limit:]
	}
	return fmt.Sprintf("status: %s exit: %d\n%s", j.Status, j.ExitCode, out), nil
}

type cancelTool struct{ mgr *Manager }

func (t *cancelTool) Name() string { return "job_cancel" }
func (t *cancelTool) Spec() llm.Tool {
	return llm.Tool{Name: "job_cancel", Description: "Cancel a running background job.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"id": map[string]any{"type": "string"},
		}, "required": []string{"id"}}}
}
func (t *cancelTool) Run(ctx context.Context, env tools.Env, args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if err := t.mgr.Cancel(in.ID); err != nil {
		return "", err
	}
	return "cancelled " + in.ID, nil
}
