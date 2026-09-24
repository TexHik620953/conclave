package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/texhik/conclave/conclave-agent/internal/config"
	"github.com/texhik/conclave/conclave-agent/internal/conn"
	"github.com/texhik/conclave/conclave-agent/internal/corehttp"
	"github.com/texhik/conclave/conclave-agent/internal/hub"
	"github.com/texhik/conclave/conclave-agent/internal/tools"
)

func newTestServer(t *testing.T) (*httptest.Server, *hub.Hub) {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s1","title":"demo","status":"active"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	t.Cleanup(core.Close)

	cfg := config.Config{CoreURL: core.URL, Workspace: t.TempDir(), ListenAddr: "127.0.0.1:0"}
	reg := tools.NewRegistry(cfg.Workspace, 0)
	h := hub.New()
	cn := conn.New(cfg, "dev-1", reg, h, nil)
	srv := New(Deps{Config: cfg, Conn: cn, Hub: h, Core: corehttp.New(core.URL, "user", "device"), Tools: reg})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, h
}

func TestStatus(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["device_id"] != "dev-1" {
		t.Fatalf("device_id = %v", out["device_id"])
	}
	if out["agent_connected"] != false {
		t.Fatalf("agent_connected = %v", out["agent_connected"])
	}
}

func TestProxySessions(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions = %d", resp.StatusCode)
	}
	var out []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out) != 1 || out[0]["id"] != "s1" {
		t.Fatalf("unexpected proxy result: %+v", out)
	}
}

func TestEventsSSE(t *testing.T) {
	ts, h := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d", resp.StatusCode)
	}
	// Give the handler a moment to subscribe, then publish.
	go func() {
		for i := 0; i < 50; i++ {
			h.Publish([]byte(`{"type":"event","session_id":"s1"}`))
		}
	}()
	buf := make([]byte, 256)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no SSE data")
	}
}
