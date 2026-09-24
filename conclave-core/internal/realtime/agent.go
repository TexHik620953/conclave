package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/stream"
	"github.com/texhik/conclave/conclave-core/internal/toolgw"
)

// agentChannelPrefix is the per-device channel agents subscribe to.
const agentChannelPrefix = "agent:device:"

// DeviceAgentID returns the agent channel for a device.
func DeviceAgentID(deviceID string) string { return agentChannelPrefix + deviceID }

// ToolCall is a request sent to an agent to execute a tool.
type ToolCall struct {
	CallID         string          `json:"call_id"`
	Name           string          `json:"name"`
	Args           json.RawMessage `json:"args,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	TimeoutMS      int             `json:"timeout_ms,omitempty"`
}

// ToolResultPayload is an agent's reply to a ToolCall.
type ToolResultPayload struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Content string `json:"content,omitempty"`
}

type agentIdentity struct {
	DeviceID string `json:"device_id"`
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
}

// AgentNode serves device-scoped agent connections and routes tool calls to
// them with correlation and timeouts.
type AgentNode struct {
	Store store.Store
	cfg   Config
	node  *centrifuge.Node

	mu           sync.Mutex
	pending      map[string]chan ToolResultPayload
	pendingCalls map[string]pendingCall
	tools        map[string][]llmgw.ToolDef
}

type pendingCall struct {
	deviceID string
	payload  []byte
}

// NewAgentNode creates and starts the agent realtime node.
func NewAgentNode(cfg Config, authMgr *auth.Manager, st store.Store) (*AgentNode, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HistorySize <= 0 {
		cfg.HistorySize = 1000
	}
	if cfg.HistoryTTL <= 0 {
		cfg.HistoryTTL = time.Hour
	}
	node, err := centrifuge.New(centrifuge.Config{
		Name:       "conclave-core-agent-" + uuid.NewString()[:8],
		LogLevel:   centrifuge.LogLevelWarn,
		LogHandler: centrifugeLog(cfg.Logger),
	})
	if err != nil {
		return nil, err
	}
	if err := setBroker(node, cfg); err != nil {
		return nil, err
	}
	a := &AgentNode{
		Store: st, cfg: cfg, node: node,
		pending:      map[string]chan ToolResultPayload{},
		pendingCalls: map[string]pendingCall{},
		tools:        map[string][]llmgw.ToolDef{},
	}
	a.setup(authMgr)
	if err := node.Run(); err != nil {
		return nil, fmt.Errorf("realtime: agent run: %w", err)
	}
	return a, nil
}

func (a *AgentNode) setup(authMgr *auth.Manager) {
	a.node.OnConnecting(func(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
		if e.Token == "" {
			return centrifuge.ConnectReply{}, centrifuge.ErrorUnauthorized
		}
		dev, err := authMgr.Authenticate(ctx, e.Token)
		if err != nil {
			return centrifuge.ConnectReply{}, centrifuge.ErrorUnauthorized
		}
		info, _ := json.Marshal(agentIdentity{DeviceID: dev.ID, UserID: dev.UserID, TenantID: dev.TenantID})
		return centrifuge.ConnectReply{Credentials: &centrifuge.Credentials{UserID: dev.ID, Info: info}}, nil
	})

	a.node.OnConnect(func(client *centrifuge.Client) {
		var id agentIdentity
		_ = json.Unmarshal(client.Info(), &id)

		// Re-deliver tool calls that were in flight when this agent last
		// disconnected; the idempotency key lets the agent deduplicate.
		a.resendPending(id.DeviceID)

		client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
			if e.Channel != DeviceAgentID(id.DeviceID) {
				cb(centrifuge.SubscribeReply{}, centrifuge.ErrorPermissionDenied)
				return
			}
			cb(centrifuge.SubscribeReply{Options: centrifuge.SubscribeOptions{EnableRecovery: true}}, nil)
		})

		client.OnRPC(func(e centrifuge.RPCEvent, cb centrifuge.RPCCallback) {
			switch e.Method {
			case "session.open":
				var req struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(e.Data, &req); err != nil || req.SessionID == "" {
					cb(centrifuge.RPCReply{}, centrifuge.ErrorBadRequest)
					return
				}
				if err := a.Store.SetSessionAgent(context.Background(), req.SessionID, id.DeviceID); err != nil {
					cb(centrifuge.RPCReply{}, centrifuge.ErrorInternal)
					return
				}
				cb(centrifuge.RPCReply{Data: []byte(`{"ok":true}`)}, nil)
			case "tools.register":
				var req struct {
					Tools []llmgw.ToolDef `json:"tools"`
				}
				if err := json.Unmarshal(e.Data, &req); err != nil {
					cb(centrifuge.RPCReply{}, centrifuge.ErrorBadRequest)
					return
				}
				a.setTools(id.DeviceID, req.Tools)
				cb(centrifuge.RPCReply{Data: []byte(`{"ok":true}`)}, nil)
			case "tool.result":
				var res ToolResultPayload
				if err := json.Unmarshal(e.Data, &res); err != nil {
					cb(centrifuge.RPCReply{}, centrifuge.ErrorBadRequest)
					return
				}
				a.resolve(res)
				cb(centrifuge.RPCReply{Data: []byte(`{"ok":true}`)}, nil)
			default:
				cb(centrifuge.RPCReply{}, centrifuge.ErrorMethodNotFound)
			}
		})
	})
}

// resendPending re-publishes in-flight tool calls for a device (used after the
// agent reconnects).
func (a *AgentNode) resendPending(deviceID string) {
	a.mu.Lock()
	var payloads [][]byte
	for _, pc := range a.pendingCalls {
		if pc.deviceID == deviceID {
			payloads = append(payloads, pc.payload)
		}
	}
	a.mu.Unlock()
	for _, p := range payloads {
		_ = a.publishDevice(deviceID, p)
	}
}

func (a *AgentNode) resolve(res ToolResultPayload) {
	a.mu.Lock()
	ch, ok := a.pending[res.CallID]
	if ok {
		delete(a.pending, res.CallID)
		delete(a.pendingCalls, res.CallID)
	}
	a.mu.Unlock()
	if ok {
		select {
		case ch <- res:
		default:
		}
	}
}

func (a *AgentNode) setTools(deviceID string, defs []llmgw.ToolDef) {
	a.mu.Lock()
	a.tools[deviceID] = defs
	a.mu.Unlock()
}

// ToolDefs returns the tool definitions advertised by the agent bound to the
// session, if any.
func (a *AgentNode) ToolDefs(sessionID string) []llmgw.ToolDef {
	deviceID, err := a.Store.GetSessionAgent(context.Background(), sessionID)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llmgw.ToolDef{}, a.tools[deviceID]...)
}

// Call routes a tool call to the agent bound to the session and waits for the
// result.
func (a *AgentNode) Call(ctx context.Context, sessionID, name string, args json.RawMessage) (ToolResultPayload, error) {
	deviceID, err := a.Store.GetSessionAgent(ctx, sessionID)
	if err != nil {
		return ToolResultPayload{}, fmt.Errorf("no agent bound to session %s", sessionID)
	}
	timeout := a.cfg.CallTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callID := uuid.NewString()
	ch := make(chan ToolResultPayload, 1)
	a.mu.Lock()
	a.pending[callID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, callID)
		delete(a.pendingCalls, callID)
		a.mu.Unlock()
	}()

	payload, _ := json.Marshal(ToolCall{
		CallID: callID, Name: name, Args: args,
		IdempotencyKey: uuid.NewString(), TimeoutMS: int(timeout / time.Millisecond),
	})
	env, _ := json.Marshal(stream.Envelope{Type: "tool.call", Data: payload})
	a.mu.Lock()
	a.pendingCalls[callID] = pendingCall{deviceID: deviceID, payload: env}
	a.mu.Unlock()
	if err := a.publishDevice(deviceID, env); err != nil {
		return ToolResultPayload{}, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return ToolResultPayload{}, ctx.Err()
	case <-timer.C:
		return ToolResultPayload{}, fmt.Errorf("tool %s timed out after %s", name, timeout)
	}
}

func (a *AgentNode) publishDevice(deviceID string, payload []byte) error {
	_, err := a.node.Publish(DeviceAgentID(deviceID), payload,
		centrifuge.WithHistory(a.cfg.HistorySize, a.cfg.HistoryTTL))
	return err
}

// Handler returns the HTTP handler for agent WebSocket connections.
func (a *AgentNode) Handler() http.Handler {
	return centrifuge.NewWebsocketHandler(a.node, centrifuge.WebsocketConfig{})
}

// Shutdown stops the node.
func (a *AgentNode) Shutdown(ctx context.Context) error { return a.node.Shutdown(ctx) }

// AgentExecutor adapts an AgentNode to toolgw.Executor, reading the session id
// from the context.
type AgentExecutor struct {
	Agents *AgentNode
}

// ToolDefs returns the tool definitions of the agent bound to the context's
// session, implementing the orchestrator's dynamic-tools contract.
func (e *AgentExecutor) ToolDefs(ctx context.Context) []llmgw.ToolDef {
	sessionID := stream.SessionIDFrom(ctx)
	if sessionID == "" {
		return nil
	}
	return e.Agents.ToolDefs(sessionID)
}

// Execute implements toolgw.Executor.
func (e *AgentExecutor) Execute(ctx context.Context, call toolgw.Call) (toolgw.Result, error) {
	sessionID := stream.SessionIDFrom(ctx)
	if sessionID == "" {
		return toolgw.Result{Content: "no session context for external tool", IsError: true}, nil
	}
	res, err := e.Agents.Call(ctx, sessionID, call.Name, call.Args)
	if err != nil {
		return toolgw.Result{Content: err.Error(), IsError: true}, nil
	}
	return toolgw.Result{Content: res.Content, IsError: res.IsError}, nil
}

// setBroker configures the Centrifuge broker (memory or redis) from cfg.
func setBroker(node *centrifuge.Node, cfg Config) error {
	if cfg.RedisAddr != "" {
		shard, err := centrifuge.NewRedisShard(node, centrifuge.RedisShardConfig{Address: cfg.RedisAddr})
		if err != nil {
			return fmt.Errorf("realtime: redis shard: %w", err)
		}
		broker, err := centrifuge.NewRedisBroker(node, centrifuge.RedisBrokerConfig{
			Prefix: "conclave",
			Shards: []*centrifuge.RedisShard{shard},
		})
		if err != nil {
			return fmt.Errorf("realtime: redis broker: %w", err)
		}
		node.SetBroker(broker)
		return nil
	}
	broker, err := centrifuge.NewMemoryBroker(node, centrifuge.MemoryBrokerConfig{})
	if err != nil {
		return fmt.Errorf("realtime: memory broker: %w", err)
	}
	node.SetBroker(broker)
	return nil
}
