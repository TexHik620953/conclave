package api

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

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/protocol"
	"github.com/texhik/conclave/conclave-core/internal/session"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
	"github.com/texhik/conclave/conclave-core/internal/worker"
)

func newTestServer(t *testing.T) (*httptest.Server, Deps) {
	t.Helper()
	st := memory.New()
	evLog := events.New(st)
	mgr, err := auth.New(st, "admin")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	sup := &orchestrator.Supervisor{
		Brain: &orchestrator.FakeBrain{
			Decisions: []orchestrator.Decision{{Delegate: &orchestrator.Delegation{Role: "senior", Task: "collect"}}},
			Finish:    "# Requirements\n- sell online",
		},
		Roles:    &orchestrator.FakeRoleRunner{},
		Log:      evLog,
		MaxSteps: 5,
	}
	svc := session.NewService(st, evLog, registry, sup)

	ivCalls := 0
	svc.Interviewer = session.NewInterviewer(st, evLog, &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
		ivCalls++
		if ivCalls == 1 {
			return toolResp("ask", `{"question":"Which stack?","options":["Go","Node"]}`)
		}
		return toolResp("write_spec", `{"content":"# Spec\n- use Go"}`)
	}}, "m")
	svc.Planner = &session.Planner{
		Store: st, Log: evLog, Playbooks: registry, Service: svc, Model: "m",
		Client: &llmgw.Fake{Fn: func(llmgw.Request) llmgw.Response {
			return toolResp("set_plan", `{"nodes":[{"id":"a","playbook_id":"ba"}],"edges":[]}`)
		}},
	}
	svc.Controller = session.NewController(svc, &llmgw.Fake{Fn: func(req llmgw.Request) llmgw.Response {
		body := ""
		if len(req.Messages) > 0 {
			body = req.Messages[len(req.Messages)-1].Content
		}
		if strings.Contains(body, "(no spec yet)") {
			return toolResp("write_spec", `{"content":"# Spec\n- x"}`)
		}
		if strings.Contains(body, "status=done") {
			return toolResp("finish", `{"summary":"done"}`)
		}
		return toolResp("set_plan", `{"nodes":[{"id":"a","playbook_id":"ba"}],"edges":[]}`)
	}}, "m")

	deps := Deps{Store: st, Events: evLog, Auth: mgr, Sessions: svc, Playbooks: registry, AdminKey: "admin"}
	srv := New(deps)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, deps
}

// drainJobs runs the worker until no more jobs are claimable.
func drainJobs(t *testing.T, deps Deps) {
	t.Helper()
	w := &worker.Worker{
		Store: deps.Store, Service: deps.Sessions, Log: deps.Events,
		ID: "test-worker", Lease: time.Minute, MaxAttempts: 3,
	}
	for i := 0; i < 50; i++ {
		did, err := w.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
		if !did {
			return
		}
	}
	t.Fatal("jobs did not drain")
}

func doJSON(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestControlPlaneFlow(t *testing.T) {
	ts, deps := newTestServer(t)

	// Bootstrap requires the admin key.
	resp, _ := doJSON(t, "POST", ts.URL+"/v1/bootstrap", "", map[string]any{"tenant_name": "Acme"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap without admin key = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("POST", ts.URL+"/v1/bootstrap", strings.NewReader(`{"tenant_name":"Acme","email":"a@b.c","device_name":"laptop"}`))
	req.Header.Set("X-Admin-Key", "admin")
	req.Header.Set("Content-Type", "application/json")
	bresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var boot map[string]string
	_ = json.NewDecoder(bresp.Body).Decode(&boot)
	bresp.Body.Close()
	if bresp.StatusCode != http.StatusCreated || boot["token"] == "" {
		t.Fatalf("bootstrap failed: %d %+v", bresp.StatusCode, boot)
	}
	token := boot["token"]

	// Create a session.
	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "Shop"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d %+v", resp.StatusCode, sess)
	}
	sessionID, _ := sess["id"].(string)
	if sessionID == "" {
		t.Fatal("missing session id")
	}

	// Run the BA playbook (asynchronous: enqueued, then processed by a worker).
	resp, out := doJSON(t, "POST", ts.URL+"/v1/sessions/"+sessionID+"/runs", token, map[string]any{
		"playbook_id": "ba", "idea": "build an online shop",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run = %d %+v", resp.StatusCode, out)
	}
	if out["job"] == nil {
		t.Fatalf("run did not return a job: %+v", out)
	}
	drainJobs(t, deps)

	// Session detail exposes the artifact.
	resp, detail := doJSON(t, "GET", ts.URL+"/v1/sessions/"+sessionID, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session = %d", resp.StatusCode)
	}
	arts, _ := detail["artifacts"].([]any)
	if len(arts) != 1 {
		t.Fatalf("artifacts = %v", detail["artifacts"])
	}

	// Events are available and resumable.
	ereq, _ := http.NewRequest("GET", ts.URL+"/v1/sessions/"+sessionID+"/events", nil)
	ereq.Header.Set("Authorization", "Bearer "+token)
	eresp, err := http.DefaultClient.Do(ereq)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	_ = json.NewDecoder(eresp.Body).Decode(&evs)
	eresp.Body.Close()
	if eresp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d", eresp.StatusCode)
	}
	if len(evs) == 0 {
		t.Fatal("no events returned")
	}

	// Unauthenticated access is rejected.
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/sessions/"+sessionID, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated get = %d, want 401", resp.StatusCode)
	}
}

func TestWebSocketHandshakeAndResume(t *testing.T) {
	ts, deps := newTestServer(t)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/bootstrap", strings.NewReader(`{"tenant_name":"Acme"}`))
	req.Header.Set("X-Admin-Key", "admin")
	req.Header.Set("Content-Type", "application/json")
	bresp, _ := http.DefaultClient.Do(req)
	var boot map[string]string
	_ = json.NewDecoder(bresp.Body).Decode(&boot)
	bresp.Body.Close()
	token := boot["token"]

	resp, sess := doJSON(t, "POST", ts.URL+"/v1/sessions", token, map[string]any{"title": "ws"})
	_ = resp
	sessionID, _ := sess["id"].(string)
	doJSON(t, "POST", ts.URL+"/v1/sessions/"+sessionID+"/runs", token, map[string]any{"playbook_id": "ba", "idea": "x"})
	drainJobs(t, deps)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?token=" + token
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	codec := protocol.Codec{}
	readFrame := func() protocol.Frame {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f, err := codec.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := readFrame(); f.Type != protocol.TypeWelcome {
		t.Fatalf("first frame = %s, want welcome", f.Type)
	}
	// hello -> welcome
	hello := protocol.Frame{Type: protocol.TypeHello, ID: "h1"}
	_ = protocol.EncodeData(&hello, protocol.Hello{Protocol: protocol.Version})
	hdata, _ := codec.Encode(hello)
	if err := conn.Write(ctx, websocket.MessageText, hdata); err != nil {
		t.Fatal(err)
	}
	if f := readFrame(); f.Type != protocol.TypeWelcome || f.ReplyTo != "h1" {
		t.Fatalf("hello ack = %+v", f)
	}
	// resume -> event frames
	resume := protocol.Frame{Type: protocol.TypeResume, SessionID: sessionID}
	_ = protocol.EncodeData(&resume, protocol.ResumeRequest{FromSeq: 0})
	rdata, _ := codec.Encode(resume)
	if err := conn.Write(ctx, websocket.MessageText, rdata); err != nil {
		t.Fatal(err)
	}
	f := readFrame()
	if f.Type != protocol.TypeEventAppend {
		t.Fatalf("resume first frame = %s, want event.append", f.Type)
	}
	if f.Seq == 0 {
		t.Fatal("event frame missing seq")
	}
}
