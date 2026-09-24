package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge-go"

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func TestClientNodePublishSubscribe(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, domain.Session{ID: "s2", TenantID: "other", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.New(st, "secret")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := mgr.RegisterUserToken(ctx, "t", "u")
	if err != nil {
		t.Fatal(err)
	}

	cn, err := NewClientNode(Config{}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cn.Shutdown(ctx) }()

	ts := httptest.NewServer(cn.Handler())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	client := centrifuge.NewJsonClient(wsURL, centrifuge.Config{Token: token})
	defer client.Close()

	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not connect")
	}

	sub, err := client.NewSubscription(channelPrefixSession + "s1")
	if err != nil {
		t.Fatal(err)
	}
	pubs := make(chan []byte, 16)
	sub.OnPublication(func(e centrifuge.PublicationEvent) { pubs <- e.Data })
	subscribed := make(chan struct{})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { close(subscribed) })
	if err := sub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription not established")
	}

	if err := cn.PublishSession("s1", []byte(`{"type":"event"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-pubs:
		if !strings.Contains(string(d), `"type":"event"`) {
			t.Fatalf("unexpected publication: %s", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publication not received")
	}

	// Subscribing to another tenant's session channel is denied.
	sub2, err := client.NewSubscription(channelPrefixSession + "s2")
	if err != nil {
		t.Fatal(err)
	}
	subErr := make(chan error, 1)
	sub2.OnError(func(e centrifuge.SubscriptionErrorEvent) { subErr <- e.Error })
	_ = sub2.Subscribe()
	select {
	case e := <-subErr:
		if e == nil {
			t.Fatal("expected subscription error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected subscription to be rejected")
	}
}

func TestRelayForwardsEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := memory.New()
	if err := st.CreateTenant(ctx, domain.Tenant{ID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	mgr, _ := auth.New(st, "secret")
	token, _, _ := mgr.RegisterUserToken(ctx, "t", "u")
	evLog := events.New(st)

	cn, err := NewClientNode(Config{}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cn.Shutdown(context.Background()) }()
	relay := &Relay{Log: evLog, Node: cn}
	go relay.Run(ctx)

	ts := httptest.NewServer(cn.Handler())
	defer ts.Close()
	client := centrifuge.NewJsonClient("ws"+strings.TrimPrefix(ts.URL, "http"), centrifuge.Config{Token: token})
	defer client.Close()
	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}
	sub, _ := client.NewSubscription(channelPrefixSession + "s1")
	pubs := make(chan []byte, 16)
	sub.OnPublication(func(e centrifuge.PublicationEvent) { pubs <- e.Data })
	subscribed := make(chan struct{})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { close(subscribed) })
	if err := sub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("not subscribed")
	}

	if _, err := evLog.Append(ctx, domain.Event{SessionID: "s1", Type: "test.event", Payload: map[string]any{"n": 1}}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-pubs:
		if !strings.Contains(string(d), "test.event") {
			t.Fatalf("unexpected publication: %s", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event not relayed")
	}
}

func TestClientNodeDeltaChannel(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateTenant(ctx, domain.Tenant{ID: "t"})
	_ = st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"})
	_ = st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive})
	mgr, _ := auth.New(st, "secret")
	token, _, _ := mgr.RegisterUserToken(ctx, "t", "u")

	cn, err := NewClientNode(Config{}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cn.Shutdown(ctx) }()
	ts := httptest.NewServer(cn.Handler())
	defer ts.Close()

	client := centrifuge.NewJsonClient("ws"+strings.TrimPrefix(ts.URL, "http"), centrifuge.Config{Token: token})
	defer client.Close()
	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}
	sub, _ := client.NewSubscription(channelPrefixSession + "s1" + deltaSuffix)
	pubs := make(chan []byte, 16)
	sub.OnPublication(func(e centrifuge.PublicationEvent) { pubs <- e.Data })
	subscribed := make(chan struct{})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { close(subscribed) })
	if err := sub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("not subscribed to delta channel")
	}
	if err := cn.PublishDelta("s1", []byte(`{"type":"message.delta","text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-pubs:
		if !strings.Contains(string(d), "message.delta") {
			t.Fatalf("unexpected delta: %s", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delta not received")
	}
}

func TestClientNodeSendsSnapshotOnSubscribe(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateTenant(ctx, domain.Tenant{ID: "t"})
	_ = st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"})
	_ = st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive})
	_ = st.CreatePlan(ctx, domain.Plan{ID: "p1", SessionID: "s1", Version: 1, Status: domain.PlanActive})
	_ = st.CreatePlanNode(ctx, domain.PlanNode{ID: "n1", Key: "a", PlanID: "p1", Kind: domain.NodeKindPlaybook, State: domain.NodePending})
	_ = st.CreateQuestion(ctx, domain.Question{ID: "q1", SessionID: "s1", Kind: domain.QuestionKindGate, Text: "Approve?", State: domain.QuestionOpen})
	mgr, _ := auth.New(st, "secret")
	token, _, _ := mgr.RegisterUserToken(ctx, "t", "u")

	cn, err := NewClientNode(Config{}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cn.Shutdown(ctx) }()
	ts := httptest.NewServer(cn.Handler())
	defer ts.Close()
	client := centrifuge.NewJsonClient("ws"+strings.TrimPrefix(ts.URL, "http"), centrifuge.Config{Token: token})
	defer client.Close()
	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}

	sub, _ := client.NewSubscription(channelPrefixSession + "s1")
	var mu sync.Mutex
	seen := map[string]bool{}
	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		var env struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e.Data, &env)
		mu.Lock()
		seen[env.Type] = true
		mu.Unlock()
	})
	subscribed := make(chan struct{})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { close(subscribed) })
	if err := sub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("not subscribed")
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !seen["plan.snapshot"] || !seen["question.asked"] {
		t.Fatalf("snapshot publications = %v", seen)
	}
}

func TestCrossReplicaRedis(t *testing.T) {
	addr := getenv("CORE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("CORE_TEST_REDIS_ADDR not set; skipping redis cross-replica")
	}
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateTenant(ctx, domain.Tenant{ID: "t"})
	_ = st.CreateUser(ctx, domain.User{ID: "u", TenantID: "t"})
	_ = st.CreateSession(ctx, domain.Session{ID: "s1", TenantID: "t", Status: domain.SessionActive})
	mgr, _ := auth.New(st, "secret")
	token, _, _ := mgr.RegisterUserToken(ctx, "t", "u")

	nodeA, err := NewClientNode(Config{RedisAddr: addr}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nodeA.Shutdown(ctx) }()
	nodeB, err := NewClientNode(Config{RedisAddr: addr}, mgr, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nodeB.Shutdown(ctx) }()

	tsB := httptest.NewServer(nodeB.Handler())
	defer tsB.Close()
	client := centrifuge.NewJsonClient("ws"+strings.TrimPrefix(tsB.URL, "http"), centrifuge.Config{Token: token})
	defer client.Close()
	connected := make(chan struct{})
	client.OnConnected(func(centrifuge.ConnectedEvent) { close(connected) })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}
	sub, _ := client.NewSubscription(channelPrefixSession + "s1")
	pubs := make(chan []byte, 16)
	sub.OnPublication(func(e centrifuge.PublicationEvent) { pubs <- e.Data })
	subscribed := make(chan struct{})
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { close(subscribed) })
	if err := sub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("not subscribed")
	}
	// Give the broker a moment to wire up the Redis subscription.
	time.Sleep(200 * time.Millisecond)

	if err := nodeA.PublishSession("s1", []byte(`{"type":"event","note":"from A"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-pubs:
		if !strings.Contains(string(d), "from A") {
			t.Fatalf("unexpected publication: %s", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cross-replica publication not received")
	}
}

func getenv(key string) string { return os.Getenv(key) }
