package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge-go"

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func TestAgentNodeToolCallRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateTenant(ctx, domain.Tenant{ID: "t"})
	_ = st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"})
	_ = st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive})
	mgr, _ := auth.New(st, "secret")
	deviceToken, dev, err := mgr.RegisterDevice(ctx, "t", "u", "laptop")
	if err != nil {
		t.Fatal(err)
	}

	an, err := NewAgentNode(Config{CallTimeout: 3 * time.Second}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = an.Shutdown(ctx) }()
	ts := httptest.NewServer(an.Handler())
	defer ts.Close()

	client := centrifuge.NewJsonClient("ws"+strings.TrimPrefix(ts.URL, "http"), centrifuge.Config{Token: deviceToken})
	defer client.Close()
	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("agent not connected")
	}

	sub, err := client.NewSubscription(DeviceAgentID(dev.ID))
	if err != nil {
		t.Fatal(err)
	}
	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(e.Data, &env)
		if env.Type != "tool.call" {
			return
		}
		var tc struct {
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		}
		_ = json.Unmarshal(env.Data, &tc)
		res, _ := json.Marshal(map[string]any{"call_id": tc.CallID, "name": tc.Name, "content": "file contents"})
		_, _ = client.RPC(context.Background(), "tool.result", res)
	})
	subscribed := make(chan struct{})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { close(subscribed) })
	if err := sub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("agent not subscribed")
	}

	// Bind the session to this device.
	open, _ := json.Marshal(map[string]any{"session_id": "s1"})
	if _, err := client.RPC(ctx, "session.open", open); err != nil {
		t.Fatalf("session.open: %v", err)
	}

	// Caller side: route a tool call through the agent.
	res, err := an.Call(ctx, "s1", "read_file", json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError || res.Content != "file contents" {
		t.Fatalf("result = %+v", res)
	}
}

func TestAgentNodeCallWithoutBinding(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	mgr, _ := auth.New(st, "secret")
	an, err := NewAgentNode(Config{CallTimeout: time.Second}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = an.Shutdown(ctx) }()
	if _, err := an.Call(ctx, "missing", "read_file", nil); err == nil {
		t.Fatal("expected error for session without a bound agent")
	}
}

func TestAgentNodeToolDefs(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateTenant(ctx, domain.Tenant{ID: "t"})
	_ = st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"})
	_ = st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive})
	mgr, _ := auth.New(st, "secret")
	deviceToken, dev, _ := mgr.RegisterDevice(ctx, "t", "u", "laptop")
	an, err := NewAgentNode(Config{CallTimeout: time.Second}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = an.Shutdown(ctx) }()
	ts := httptest.NewServer(an.Handler())
	defer ts.Close()

	client := centrifuge.NewJsonClient("ws"+strings.TrimPrefix(ts.URL, "http"), centrifuge.Config{Token: deviceToken})
	defer client.Close()
	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("agent not connected")
	}

	reg, _ := json.Marshal(map[string]any{"tools": []map[string]any{
		{"name": "read_file", "description": "read a file", "parameters": map[string]any{"type": "object"}},
	}})
	if _, err := client.RPC(ctx, "tools.register", reg); err != nil {
		t.Fatalf("tools.register: %v", err)
	}
	open, _ := json.Marshal(map[string]any{"session_id": "s1"})
	if _, err := client.RPC(ctx, "session.open", open); err != nil {
		t.Fatalf("session.open: %v", err)
	}
	_ = dev
	defs := an.ToolDefs("s1")
	if len(defs) != 1 || defs[0].Name != "read_file" {
		t.Fatalf("defs = %+v", defs)
	}
}
