package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/store"
)

type fakeDeps struct {
	cfg   *config.Config
	bus   *event.Bus
	ranCh chan orchestrator.RunOptions
}

func (f *fakeDeps) Config() *config.Config { return f.cfg }
func (f *fakeDeps) ListRuns(int) ([]store.Run, error) {
	return []store.Run{{ID: "r1", Pipeline: "t", Status: "completed"}}, nil
}
func (f *fakeDeps) GetRun(id string) (*store.Run, error) {
	return &store.Run{ID: id, Pipeline: "t", Status: "completed"}, nil
}
func (f *fakeDeps) ListNodes(string) ([]store.NodeRecord, error)        { return nil, nil }
func (f *fakeDeps) ListArtifacts(string) ([]store.Artifact, error)      { return nil, nil }
func (f *fakeDeps) ListEvents(string, int) ([]store.EventRecord, error) { return nil, nil }
func (f *fakeDeps) ArtifactContent(string, string) (string, error)      { return "", nil }
func (f *fakeDeps) Bus() *event.Bus                                     { return f.bus }
func (f *fakeDeps) RunPipeline(ctx context.Context, opts orchestrator.RunOptions) (*orchestrator.RunResult, error) {
	f.ranCh <- opts
	f.bus.Publish(event.Event{Type: event.RunStarted, RunID: opts.RunID, Message: opts.Pipeline})
	return &orchestrator.RunResult{RunID: opts.RunID, Status: "completed"}, nil
}
func (f *fakeDeps) AskRole(context.Context, string, string, string, string, string) error { return nil }

func testServer(token string) (*Server, *fakeDeps) {
	deps := &fakeDeps{
		cfg: &config.Config{
			Pipelines: map[string]config.Pipeline{"feature": {Description: "flow", Nodes: []config.Node{{ID: "a", Type: "agent", Role: "senior"}}}},
			Roles:     map[string]config.Role{"senior": {ID: "senior", Title: "Senior", Model: "mock/m"}},
		},
		bus:   event.NewBus(),
		ranCh: make(chan orchestrator.RunOptions, 4),
	}
	return New(context.Background(), deps, Options{Token: token}), deps
}

func TestConfigEndpoint(t *testing.T) {
	srv, _ := testServer("")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Pipelines []pipelineDTO `json:"pipelines"`
		Roles     []roleDTO     `json:"roles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pipelines) != 1 || len(body.Roles) != 1 {
		t.Fatalf("unexpected config: %+v", body)
	}
}

func TestCreateRun(t *testing.T) {
	srv, deps := testServer("")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload, _ := json.Marshal(map[string]any{"pipeline": "feature", "task": "do it"})
	resp, err := http.Post(ts.URL+"/api/runs", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	if out["run_id"] == "" {
		t.Fatal("missing run_id")
	}
	select {
	case opts := <-deps.ranCh:
		if opts.Pipeline != "feature" || opts.RunID != out["run_id"] {
			t.Fatalf("unexpected opts: %+v", opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPipeline was not called")
	}
}

func TestAuthRequired(t *testing.T) {
	srv, _ := testServer("secret")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/api/config")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/config", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestWebSocketStreamsEvents(t *testing.T) {
	srv, deps := testServer("")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, hello, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hello), "hello") {
		t.Fatalf("expected hello, got %s", hello)
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribe","run_id":"run-x"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	deps.bus.Publish(event.Event{Type: event.RoleDelta, RunID: "other", Role: "r", Message: "ignored"})
	deps.bus.Publish(event.Event{Type: event.RoleDelta, RunID: "run-x", Role: "senior", Model: "mock/m", Message: "hi"})

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"hi"`) || !strings.Contains(string(data), `"model":"mock/m"`) {
		t.Fatalf("unexpected event: %s", data)
	}
}
