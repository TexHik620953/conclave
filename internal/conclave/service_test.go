package conclave

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/store"
)

type fakeMasterClient struct {
	calls  int
	review bool
}

func (f *fakeMasterClient) Stream(ctx context.Context, m llm.Model, req llm.Request, sink func(llm.Event) error) error {
	f.calls++
	if f.calls == 1 {
		if f.review {
			_ = sink(llm.Event{Type: llm.EventTool, ToolCall: domain.ToolCall{
				ID: "finish", Name: "finish_run", Arguments: `{"summary":"all done"}`,
			}})
		} else {
			plan := domain.Plan{
				Goal: "create hello",
				Tasks: []domain.Task{{
					ID: "t1", Title: "write hello", Spec: "Write hello.txt with the word hello.",
					WorkerID: "coder", WriteScope: []string{"hello.txt"},
					Acceptance: []string{"hello.txt exists"},
				}},
			}
			b, _ := json.Marshal(plan)
			_ = sink(llm.Event{Type: llm.EventTool, ToolCall: domain.ToolCall{ID: "plan", Name: "submit_plan", Arguments: string(b)}})
		}
	} else {
		_ = sink(llm.Event{Type: llm.EventText, Text: "ok"})
	}
	_ = sink(llm.Event{Type: llm.EventUsage, Usage: domain.Usage{PromptTokens: 10, CompletionTokens: 2}})
	return sink(llm.Event{Type: llm.EventDone})
}

type fakeWorkerClient struct{ calls int }

func (f *fakeWorkerClient) Stream(ctx context.Context, m llm.Model, req llm.Request, sink func(llm.Event) error) error {
	f.calls++
	if f.calls == 1 {
		_ = sink(llm.Event{Type: llm.EventTool, ToolCall: domain.ToolCall{
			ID: "write", Name: "write_file", Arguments: `{"path":"hello.txt","content":"hello\n"}`,
		}})
	} else {
		_ = sink(llm.Event{Type: llm.EventText, Text: "created hello.txt"})
	}
	_ = sink(llm.Event{Type: llm.EventUsage, Usage: domain.Usage{PromptTokens: 20, CompletionTokens: 4}})
	return sink(llm.Event{Type: llm.EventDone})
}

func TestConclaveEndToEnd(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Providers: map[string]config.Provider{
		"fake": {BaseURL: "http://fake", Models: map[string]config.Model{
			"master": {Context: 100000, MaxOutput: 1000},
			"worker": {Context: 100000, MaxOutput: 1000},
		}},
	}}
	profile := config.Profile{
		Master:  config.AgentConfig{Model: "fake/master", Effort: "high"},
		Agent:   config.AgentConfig{Model: "fake/worker", Effort: "medium"},
		Workers: []config.WorkerConfig{{ID: "coder", Model: "fake/worker", Instances: 1}},
		Limits:  config.Limits{MaxParallel: 2, MaxIterations: 3},
	}
	bus := event.New()
	masterReview := false
	svc := New(Deps{
		Config: cfg, Profile: profile, Store: st, Bus: bus, SessionID: "ses_test", Cwd: dir,
		Client: func(ref string) (llm.Client, llm.Model, error) {
			m, err := cfg.ResolveModel(ref)
			if err != nil {
				return nil, llm.Model{}, err
			}
			model := llm.ModelFromRef(ref, m)
			if ref == "fake/master" {
				c := &fakeMasterClient{review: masterReview}
				masterReview = true
				return c, model, nil
			}
			return &fakeWorkerClient{}, model, nil
		},
	})
	if err := svc.Send(context.Background(), "make hello.txt"); err != nil {
		t.Fatal(err)
	}
	if svc.Snapshot().Run.Phase != domain.RunApproval {
		t.Fatalf("phase = %s, want approval", svc.Snapshot().Run.Phase)
	}
	if err := svc.Approve(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		snap := svc.Snapshot()
		if snap.Run.Phase == domain.RunCompleted {
			break
		}
		if snap.Run.Phase == domain.RunFailed {
			t.Fatalf("run failed: %s", snap.Run.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	snap := svc.Snapshot()
	if snap.Run.Phase != domain.RunCompleted {
		t.Fatalf("phase = %s, error = %s", snap.Run.Phase, snap.Run.Error)
	}
	for _, task := range snap.Run.Tasks {
		t.Logf("task %s status=%s err=%s branch=%s commit=%s", task.ID, task.Status, task.Error, task.Branch, task.Commit)
	}
	t.Logf("integration=%s branch=%s", snap.Run.IntegrationDir, snap.Run.IntegrationBranch)
	if snap.Run.IntegrationDir == "" {
		t.Fatal("no integration worktree")
	}
	b, err := os.ReadFile(filepath.Join(snap.Run.IntegrationDir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("content = %q", b)
	}
	if snap.Run.Tasks[0].Status != domain.TaskDone {
		t.Fatalf("task status = %s (%s)", snap.Run.Tasks[0].Status, snap.Run.Tasks[0].Error)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
