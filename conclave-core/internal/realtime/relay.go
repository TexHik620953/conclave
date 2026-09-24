package realtime

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/stream"
)

// Relay forwards events from the event log to Centrifuge channels. It runs once
// per process; cross-replica delivery is handled by the Centrifuge broker.
type Relay struct {
	Log    *events.Log
	Node   *ClientNode
	Logger *slog.Logger
}

// Run forwards events until the context is cancelled.
func (r *Relay) Run(ctx context.Context) {
	ch, cancel := r.Log.Subscribe(4096)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			r.publish(ev)
		}
	}
}

func (r *Relay) publish(ev domain.Event) {
	if ev.SessionID == "" {
		return
	}
	e := ev
	data, err := json.Marshal(stream.Envelope{Type: "event", SessionID: ev.SessionID, Event: &e})
	if err != nil {
		return
	}
	if err := r.Node.PublishSession(ev.SessionID, data); err != nil {
		r.log().Warn("relay publish failed", "session", ev.SessionID, "error", err.Error())
	}
}

func (r *Relay) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
