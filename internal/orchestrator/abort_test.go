package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/store"
)

func TestRunAbortedOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open("", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: srv.URL + "/v1"}},
		Roles:     map[string]config.Role{"r": {ID: "r", Model: "mock/m"}},
		Pipelines: map[string]config.Pipeline{
			"t": {Start: "a", Nodes: []config.Node{{ID: "a", Type: "agent", Role: "r", Output: "a.md"}}},
		},
		Settings: config.Settings{MaxParallel: 1, MaxIterations: 1, Timeout: config.Duration(5 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Cfg:   cfg,
		LLM:   llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		Store: st,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	res, err := e.Run(ctx, RunOptions{Pipeline: "t", Task: "x", Workspace: dir})
	if err == nil {
		t.Fatal("expected error on cancellation")
	}
	if res == nil || res.Status != "aborted" {
		t.Fatalf("status = %v, want aborted", res)
	}
	run, err := st.GetRun(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "aborted" {
		t.Fatalf("stored status = %q, want aborted", run.Status)
	}
}
