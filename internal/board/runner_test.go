package board

import (
	"context"
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
	"github.com/texhik/conclave/internal/tracker"
)

type fakeClient struct{ calls int }

func (f *fakeClient) Stream(ctx context.Context, m llm.Model, req llm.Request, sink func(llm.Event) error) error {
	f.calls++
	if f.calls == 1 {
		_ = sink(llm.Event{Type: llm.EventTool, ToolCall: domain.ToolCall{ID: "w", Name: "write_file", Arguments: `{"path":"done.txt","content":"ok"}`}})
	} else {
		_ = sink(llm.Event{Type: llm.EventText, Text: "implemented"})
	}
	_ = sink(llm.Event{Type: llm.EventUsage, Usage: domain.Usage{PromptTokens: 10, CompletionTokens: 2}})
	return sink(llm.Event{Type: llm.EventDone})
}

func TestRunnerClaimsAndExecutesTask(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "a@b.c")
	run(t, dir, "config", "user.name", "a")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "init")
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tr, err := tracker.New(st.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Create(context.Background(), tracker.CreateInput{Title: "do", Description: "write file", Complexity: 1, Priority: 5, Status: tracker.StatusReady, CreatedBy: "user"}, "user"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Providers: map[string]config.Provider{"fake": {
		BaseURL: "http://fake", Models: map[string]config.Model{"m": {Context: 10000, MaxOutput: 1000}},
	}}}
	profile := config.Profile{
		Workers: []config.WorkerConfig{{ID: "coder", Model: "fake/m", Complexity: []int{1}, Instances: 1}},
		Limits:  config.Limits{MaxParallel: 1},
	}
	r := New(Deps{Config: cfg, Profile: profile, Tracker: tr, Bus: event.New(), Cwd: dir, Client: func(ref string) (llm.Client, llm.Model, error) {
		return &fakeClient{}, llm.Model{Provider: "fake", ID: "m", Context: 10000}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tasks, _ := tr.List(context.Background(), tracker.Filter{})
		if len(tasks) == 1 && tasks[0].Status == tracker.StatusReview {
			if tasks[0].Branch == "" || tasks[0].Commit == "" {
				t.Fatalf("execution metadata = %#v", tasks[0])
			}
			attempts, _ := tr.Attempts(context.Background(), tasks[0].ID)
			if len(attempts) != 1 {
				t.Fatalf("attempts = %d", len(attempts))
			}
			entries, _ := tr.Logs(context.Background(), tasks[0].ID, attempts[0].ID)
			if len(entries) == 0 {
				t.Fatal("task logs are empty")
			}
			r.Pause()
			return
		}
		if len(tasks) == 1 && tasks[0].Status == tracker.StatusBlocked {
			t.Fatalf("task blocked: %s", tasks[0].Context)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("task did not reach review")
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}
