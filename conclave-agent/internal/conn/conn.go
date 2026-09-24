// Package conn maintains the two Centrifuge connections an agent needs: the
// device-scoped agent connection (tool calls) and the user-scoped client
// connection (events), forwarding events to the local UI hub.
package conn

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge-go"

	"github.com/texhik/conclave/conclave-agent/internal/config"
	"github.com/texhik/conclave/conclave-agent/internal/hub"
	"github.com/texhik/conclave/conclave-agent/internal/tools"
)

const (
	agentChannelPrefix  = "agent:device:"
	clientSessionPrefix = "client:session:"
	deltaSuffix         = ":delta"
)

// Conn holds both Centrifuge connections.
type Conn struct {
	cfg      config.Config
	deviceID string
	tools    *tools.Registry
	hub      *hub.Hub
	log      *slog.Logger

	agent    *centrifuge.Client
	client   *centrifuge.Client
	agentSub *centrifuge.Subscription

	mu   sync.Mutex
	subs map[string]bool
}

// New creates a connection manager.
func New(cfg config.Config, deviceID string, reg *tools.Registry, h *hub.Hub, log *slog.Logger) *Conn {
	if log == nil {
		log = slog.Default()
	}
	return &Conn{cfg: cfg, deviceID: deviceID, tools: reg, hub: h, log: log, subs: map[string]bool{}}
}

// DeviceID returns the device id.
func (c *Conn) DeviceID() string { return c.deviceID }

// AgentConnected reports whether the agent WebSocket is connected.
func (c *Conn) AgentConnected() bool {
	return c.agent != nil && c.agent.State() == centrifuge.StateConnected
}

// ClientConnected reports whether the client WebSocket is connected.
func (c *Conn) ClientConnected() bool {
	return c.client != nil && c.client.State() == centrifuge.StateConnected
}

// Start connects both WebSockets.
func (c *Conn) Start(ctx context.Context) error {
	if err := c.startAgent(ctx); err != nil {
		return err
	}
	c.startClient(ctx)
	return nil
}

func (c *Conn) startAgent(ctx context.Context) error {
	client := centrifuge.NewJsonClient(c.cfg.WSURL("/ws/agent"), centrifuge.Config{Token: c.cfg.DeviceToken})
	c.agent = client
	client.OnConnected(func(centrifuge.ConnectedEvent) {
		c.log.Info("agent connected", "device", c.deviceID)
		// Run in a goroutine: the handler runs on the read loop, so blocking on
		// an RPC here would prevent the reply from being processed.
		go c.registerTools()
	})
	client.OnDisconnected(func(centrifuge.DisconnectedEvent) {
		c.log.Warn("agent disconnected")
	})
	sub, err := client.NewSubscription(agentChannelPrefix + c.deviceID)
	if err != nil {
		return err
	}
	sub.OnPublication(func(e centrifuge.PublicationEvent) { c.onAgentPublication(e.Data) })
	c.agentSub = sub
	if err := sub.Subscribe(); err != nil {
		return err
	}
	return client.Connect()
}

func (c *Conn) startClient(ctx context.Context) {
	client := centrifuge.NewJsonClient(c.cfg.WSURL("/ws/client"), centrifuge.Config{Token: c.cfg.UserToken})
	c.client = client
	client.OnConnected(func(centrifuge.ConnectedEvent) {
		c.log.Info("client connected")
		go func() {
			c.mu.Lock()
			sessions := make([]string, 0, len(c.subs))
			for s := range c.subs {
				sessions = append(sessions, s)
			}
			c.mu.Unlock()
			for _, s := range sessions {
				_ = c.subscribe(s)
			}
		}()
	})
	client.OnDisconnected(func(centrifuge.DisconnectedEvent) { c.log.Warn("client disconnected") })
	_ = client.Connect()
}

func (c *Conn) registerTools() {
	defs := c.tools.Definitions()
	data, _ := json.Marshal(map[string]any{"tools": defs})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.agent.RPC(ctx, "tools.register", data); err != nil {
		c.log.Warn("tools.register failed", "error", err.Error())
		return
	}
	c.log.Info("tools registered", "count", len(defs))
}

// OpenSession binds a session to this device on the core.
func (c *Conn) OpenSession(ctx context.Context, sessionID string) error {
	if c.agent == nil {
		return errNotConnected
	}
	data, _ := json.Marshal(map[string]any{"session_id": sessionID})
	_, err := c.agent.RPC(ctx, "session.open", data)
	return err
}

// Subscribe subscribes the client connection to a session's channels.
func (c *Conn) Subscribe(sessionID string) error {
	c.mu.Lock()
	c.subs[sessionID] = true
	c.mu.Unlock()
	return c.subscribe(sessionID)
}

func (c *Conn) subscribe(sessionID string) error {
	if c.client == nil {
		return errNotConnected
	}
	for _, ch := range []string{clientSessionPrefix + sessionID, clientSessionPrefix + sessionID + deltaSuffix} {
		sub, err := c.client.NewSubscription(ch)
		if err != nil {
			return err
		}
		sub.OnPublication(func(e centrifuge.PublicationEvent) { c.hub.Publish(e.Data) })
		if err := sub.Subscribe(); err != nil {
			return err
		}
	}
	return nil
}

// Unsubscribe stops forwarding a session's events.
func (c *Conn) Unsubscribe(sessionID string) {
	c.mu.Lock()
	delete(c.subs, sessionID)
	c.mu.Unlock()
}

// Close closes both connections.
func (c *Conn) Close() {
	if c.agent != nil {
		c.agent.Close()
	}
	if c.client != nil {
		c.client.Close()
	}
}

type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type toolCall struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
}

type toolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	IsError bool   `json:"is_error"`
	Content string `json:"content"`
}

func (c *Conn) onAgentPublication(data []byte) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	if env.Type != "tool.call" {
		return
	}
	var tc toolCall
	if err := json.Unmarshal(env.Data, &tc); err != nil {
		return
	}
	go c.runTool(tc)
}

func (c *Conn) runTool(tc toolCall) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.CommandTimeout+30*time.Second)
	defer cancel()
	res, err := c.tools.Execute(ctx, tc.Name, tc.Args)
	if err != nil {
		res = tools.Result{Content: err.Error(), IsError: true}
	}
	c.log.Info("tool executed", "tool", tc.Name, "error", res.IsError)
	reply, _ := json.Marshal(toolResult{
		CallID: tc.CallID, Name: tc.Name, IsError: res.IsError, Content: truncate(res.Content),
	})
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rpcCancel()
	if _, err := c.agent.RPC(rpcCtx, "tool.result", reply); err != nil {
		c.log.Warn("tool.result failed", "error", err.Error())
	}
}

func truncate(s string) string {
	const max = 256 * 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}

// errNotConnected is returned when an RPC is attempted before connect.
var errNotConnected = errConnNotConnected{}

type errConnNotConnected struct{}

func (errConnNotConnected) Error() string { return "agent connection is not ready" }
