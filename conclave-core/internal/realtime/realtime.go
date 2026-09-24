// Package realtime provides the client-facing WebSocket transport built on
// Centrifuge (pub/sub, history/recovery, cross-replica broker) and a relay
// that forwards the event log to subscribers.
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

// Channel prefixes. Channels are:
//
//	client:session:<session_id>  — events, plan updates, deltas, questions
//	client:user:<user_id>        — user-level notifications
const (
	channelPrefixSession = "client:session:"
	channelPrefixUser    = "client:user:"
	// deltaSuffix marks the ephemeral token-delta channel of a session.
	deltaSuffix = ":delta"
)

// Config configures the client realtime node.
type Config struct {
	// RedisAddr, when set, uses the Redis broker (cross-replica). Empty uses an
	// in-process memory broker (single node).
	RedisAddr string
	// HistorySize / HistoryTTL bound channel history for recovery.
	HistorySize int
	HistoryTTL  time.Duration
	// CallTimeout bounds a tool call to an agent (default 60s).
	CallTimeout time.Duration
	Logger      *slog.Logger
}

// ClientNode is the Centrifuge node serving client (user-scoped) connections.
type ClientNode struct {
	node  *centrifuge.Node
	cfg   Config
	store store.Store
}

type identity struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
}

// NewClientNode creates and starts the client realtime node.
func NewClientNode(cfg Config, authMgr *auth.Manager, st store.Store) (*ClientNode, error) {
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
		Name:       "conclave-core-" + uuid.NewString()[:8],
		LogLevel:   centrifuge.LogLevelWarn,
		LogHandler: centrifugeLog(cfg.Logger),
	})
	if err != nil {
		return nil, err
	}

	if err := setBroker(node, cfg); err != nil {
		return nil, err
	}

	n := &ClientNode{node: node, cfg: cfg, store: st}
	if err := n.setup(authMgr); err != nil {
		return nil, err
	}
	if err := node.Run(); err != nil {
		return nil, fmt.Errorf("realtime: run: %w", err)
	}
	return n, nil
}

func (n *ClientNode) setup(authMgr *auth.Manager) error {
	n.node.OnConnecting(func(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
		token := e.Token
		if token == "" {
			return centrifuge.ConnectReply{}, centrifuge.ErrorUnauthorized
		}
		ut, err := authMgr.AuthenticateUser(ctx, token)
		if err != nil {
			return centrifuge.ConnectReply{}, centrifuge.ErrorUnauthorized
		}
		info, _ := json.Marshal(identity{UserID: ut.UserID, TenantID: ut.TenantID})
		return centrifuge.ConnectReply{
			Credentials: &centrifuge.Credentials{UserID: ut.UserID, Info: info},
		}, nil
	})

	n.node.OnConnect(func(client *centrifuge.Client) {
		var id identity
		_ = json.Unmarshal(client.Info(), &id)

		client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
			if err := authorizeSubscribe(context.Background(), n.store, id, e.Channel); err != nil {
				cb(centrifuge.SubscribeReply{}, err)
				return
			}
			reply := centrifuge.SubscribeReply{
				Options: centrifuge.SubscribeOptions{EnableRecovery: true, EmitPresence: false},
			}
			// Deliver an initial snapshot for the main session channel (not the
			// delta channel).
			if strings.HasPrefix(e.Channel, channelPrefixSession) && !strings.HasSuffix(e.Channel, deltaSuffix) {
				sessionID := strings.TrimPrefix(e.Channel, channelPrefixSession)
				reply.Publications = n.snapshot(context.Background(), sessionID)
			}
			cb(reply, nil)
		})

		client.OnDisconnect(func(centrifuge.DisconnectEvent) {
			n.cfg.Logger.Debug("client disconnected", "user", client.UserID())
		})
	})
	return nil
}

// authorizeSubscribe checks that the identity may subscribe to the channel.
func authorizeSubscribe(ctx context.Context, st store.Store, id identity, channel string) error {
	switch {
	case strings.HasPrefix(channel, channelPrefixUser):
		if strings.TrimPrefix(channel, channelPrefixUser) != id.UserID {
			return centrifuge.ErrorPermissionDenied
		}
		return nil
	case strings.HasPrefix(channel, channelPrefixSession):
		sessionID := strings.TrimPrefix(channel, channelPrefixSession)
		sessionID = strings.TrimSuffix(sessionID, deltaSuffix)
		sess, err := st.GetSession(ctx, sessionID)
		if err != nil || sess.TenantID != id.TenantID {
			return centrifuge.ErrorPermissionDenied
		}
		return nil
	default:
		return centrifuge.ErrorPermissionDenied
	}
}

// snapshot builds the initial publications delivered on subscribe: the current
// plan state and any open questions.
func (n *ClientNode) snapshot(ctx context.Context, sessionID string) []*centrifuge.Publication {
	var pubs []*centrifuge.Publication
	if plan, err := n.store.ActivePlan(ctx, sessionID); err == nil {
		nodes, _ := n.store.ListPlanNodes(ctx, plan.ID)
		edges, _ := n.store.ListPlanEdges(ctx, plan.ID)
		data, _ := json.Marshal(map[string]any{"plan": plan, "nodes": nodes, "edges": edges})
		env, _ := json.Marshal(stream.Envelope{Type: "plan.snapshot", SessionID: sessionID, Data: data})
		pubs = append(pubs, &centrifuge.Publication{Data: env})
	}
	if questions, err := n.store.ListQuestions(ctx, sessionID); err == nil {
		for _, q := range questions {
			if q.State != domain.QuestionOpen {
				continue
			}
			data, _ := json.Marshal(q)
			env, _ := json.Marshal(stream.Envelope{Type: "question.asked", SessionID: sessionID, Data: data})
			pubs = append(pubs, &centrifuge.Publication{Data: env})
		}
	}
	return pubs
}

// Handler returns the HTTP handler for client WebSocket connections.
func (n *ClientNode) Handler() http.Handler {
	return centrifuge.NewWebsocketHandler(n.node, centrifuge.WebsocketConfig{})
}

// Shutdown stops the node.
func (n *ClientNode) Shutdown(ctx context.Context) error { return n.node.Shutdown(ctx) }

// PublishSession publishes a payload to a session channel with history.
func (n *ClientNode) PublishSession(sessionID string, payload []byte) error {
	_, err := n.node.Publish(channelPrefixSession+sessionID, payload,
		centrifuge.WithHistory(n.cfg.HistorySize, n.cfg.HistoryTTL))
	return err
}

// PublishDelta publishes an ephemeral token-delta payload to a session's delta
// channel with history (so a reconnecting client can recover recent deltas).
func (n *ClientNode) PublishDelta(sessionID string, payload []byte) error {
	_, err := n.node.Publish(channelPrefixSession+sessionID+deltaSuffix, payload,
		centrifuge.WithHistory(n.cfg.HistorySize, n.cfg.HistoryTTL))
	return err
}

// PublishUser publishes a payload to a user channel with history.
func (n *ClientNode) PublishUser(userID string, payload []byte) error {
	_, err := n.node.Publish(channelPrefixUser+userID, payload,
		centrifuge.WithHistory(n.cfg.HistorySize, n.cfg.HistoryTTL))
	return err
}

func centrifugeLog(logger *slog.Logger) centrifuge.LogHandler {
	return func(entry centrifuge.LogEntry) {
		switch entry.Level {
		case centrifuge.LogLevelError:
			logger.Error(entry.Message, "fields", entry.Fields)
		case centrifuge.LogLevelWarn:
			logger.Warn(entry.Message, "fields", entry.Fields)
		default:
			logger.Debug(entry.Message, "fields", entry.Fields)
		}
	}
}
