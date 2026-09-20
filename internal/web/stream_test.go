package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/store"
)

type engineDeps struct {
	cfg    *config.Config
	bus    *event.Bus
	engine *orchestrator.Engine
}

func (d *engineDeps) Config() *config.Config            { return d.cfg }
func (d *engineDeps) ListRuns(int) ([]store.Run, error) { return nil, nil }
func (d *engineDeps) GetRun(id string) (*store.Run, error) {
	return &store.Run{ID: id, Status: "completed"}, nil
}
func (d *engineDeps) ListNodes(string) ([]store.NodeRecord, error)   { return nil, nil }
func (d *engineDeps) ListArtifacts(string) ([]store.Artifact, error) { return nil, nil }
func (d *engineDeps) ListEvents(string, int) ([]store.EventRecord, error) {
	return nil, nil
}
func (d *engineDeps) ArtifactContent(string, string) (string, error) { return "", nil }
func (d *engineDeps) Bus() *event.Bus                                { return d.bus }
func (d *engineDeps) RunPipeline(ctx context.Context, opts orchestrator.RunOptions) (*orchestrator.RunResult, error) {
	return d.engine.Run(ctx, opts)
}
func (d *engineDeps) AskRole(context.Context, string, string, string, string, string) error {
	return nil
}

func TestStreamingThroughEngine(t *testing.T) {
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, part := range []string{"Hel", "lo ", "world"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", part)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(30 * time.Millisecond)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer llmSrv.Close()

	cfg := &config.Config{
		Providers: map[string]config.Provider{"mock": {Type: "openai", BaseURL: llmSrv.URL + "/v1"}},
		Roles:     map[string]config.Role{"r": {ID: "r", Model: "mock/m"}},
		Pipelines: map[string]config.Pipeline{
			"t": {Start: "a", Nodes: []config.Node{{ID: "a", Type: "agent", Role: "r", Output: "a.md"}}},
		},
		Settings: config.Settings{MaxParallel: 1, MaxIterations: 2, Timeout: config.Duration(5 * time.Second)},
	}
	reg, err := provider.NewRegistry(cfg.Providers)
	if err != nil {
		t.Fatal(err)
	}
	bus := event.NewBus()
	engine := &orchestrator.Engine{
		Cfg: cfg,
		LLM: llm.New(reg, 5*time.Second, llm.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}),
		Bus: bus,
	}
	srv := New(context.Background(), &engineDeps{cfg: cfg, bus: bus, engine: engine}, Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.Read(ctx) // hello
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","run_id":""}`))
	time.Sleep(50 * time.Millisecond)

	payload, _ := json.Marshal(map[string]any{"pipeline": "t", "task": "x"})
	resp, err := http.Post(ts.URL+"/api/runs", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deltas := 0
	finished := false
	for !finished {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (deltas=%d)", err, deltas)
		}
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Data    struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"data"`
		}
		json.Unmarshal(data, &ev)
		switch ev.Type {
		case "role.delta":
			deltas++
		case "context.usage":
			if ev.Data.CompletionTokens != 3 {
				t.Errorf("context.usage completion_tokens = %d, want 3", ev.Data.CompletionTokens)
			}
		case "run.finished":
			if deltas == 0 {
				t.Fatal("run finished before any streaming delta")
			}
			finished = true
		}
	}
	if deltas < 3 {
		t.Fatalf("expected >=3 streaming deltas, got %d", deltas)
	}
}
