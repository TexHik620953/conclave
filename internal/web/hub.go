// Package web implements conclave's HTTP + WebSocket server and the protocol
// used by the browser client.
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/texhik/conclave/internal/event"
)

// clientMessage is a message from the browser to the server.
type clientMessage struct {
	Type  string `json:"type"`
	RunID string `json:"run_id,omitempty"`
}

// Hub fans run events out to connected WebSocket clients.
type Hub struct {
	bus *event.Bus
}

// NewHub creates a hub over the event bus.
func NewHub(bus *event.Bus) *Hub { return &Hub{bus: bus} }

// Serve upgrades an HTTP request to a WebSocket and streams events.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	ch, cancel := h.bus.Subscribe(2048)
	defer cancel()

	c := &wsClient{conn: conn}
	if err := c.write(ctx, map[string]any{"type": "hello", "protocol": 1}); err != nil {
		return
	}

	go c.readLoop(ctx, cancel)

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !c.matches(ev) {
				continue
			}
			if err := c.write(ctx, ev); err != nil {
				return
			}
		case <-ticker.C:
			pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			cancelPing()
			if err != nil {
				return
			}
		}
	}
}

type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	run  string
}

func (c *wsClient) setFilter(runID string) {
	c.mu.Lock()
	c.run = runID
	c.mu.Unlock()
}

func (c *wsClient) matches(ev event.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run == "" || ev.RunID == c.run
}

func (c *wsClient) write(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Write(writeCtx, websocket.MessageText, data)
}

func (c *wsClient) readLoop(ctx context.Context, cancel func()) {
	defer cancel()
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		var m clientMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		switch m.Type {
		case "subscribe":
			c.setFilter(m.RunID)
		case "unsubscribe":
			c.setFilter("")
		case "ping":
			_ = c.write(ctx, map[string]any{"type": "pong"})
		}
	}
}
