package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/texhik/conclave/conclave-core/internal/protocol"
)

// handleWS upgrades a request to a WebSocket and speaks the agent protocol.
// P0 supports the handshake, ping/pong and event resume; tool proxying arrives
// with the local agent.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing token")
		return
	}
	dev, err := s.deps.Auth.Authenticate(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	codec := protocol.Codec{}

	welcome := protocol.Frame{Type: protocol.TypeWelcome}
	_ = protocol.EncodeData(&welcome, protocol.Welcome{Protocol: protocol.Version, CoreVersion: "0.1.0"})
	if err := writeFrame(ctx, conn, codec, welcome); err != nil {
		return
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		frame, err := codec.Decode(data)
		if err != nil {
			_ = writeErrorFrame(ctx, conn, codec, "decode_error", err.Error())
			continue
		}
		switch frame.Type {
		case protocol.TypeHello:
			var hello protocol.Hello
			if err := protocol.DecodeData(frame, &hello); err == nil {
				s.deps.Logger.Info("agent hello", "device", dev.ID, "os", hello.Capabilities.OS)
			}
			ack := protocol.Frame{Type: protocol.TypeWelcome, ReplyTo: frame.ID}
			_ = protocol.EncodeData(&ack, protocol.Welcome{Protocol: protocol.Version, CoreVersion: "0.1.0"})
			_ = writeFrame(ctx, conn, codec, ack)
		case protocol.TypePing:
			_ = writeFrame(ctx, conn, codec, protocol.Frame{Type: protocol.TypePong, ReplyTo: frame.ID})
		case protocol.TypeResume:
			var rr protocol.ResumeRequest
			_ = protocol.DecodeData(frame, &rr)
			evs, err := s.deps.Events.List(ctx, frame.SessionID, rr.FromSeq)
			if err != nil {
				_ = writeErrorFrame(ctx, conn, codec, "resume_error", err.Error())
				continue
			}
			for _, e := range evs {
				payload, _ := json.Marshal(e)
				f := protocol.Frame{
					Type: protocol.TypeEventAppend, SessionID: e.SessionID, Seq: e.Seq,
					Data: payload,
				}
				if err := writeFrame(ctx, conn, codec, f); err != nil {
					return
				}
			}
		default:
			_ = writeErrorFrame(ctx, conn, codec, "unknown_type", "unsupported frame type "+frame.Type)
		}
	}
}

func writeFrame(ctx context.Context, conn *websocket.Conn, codec protocol.Codec, f protocol.Frame) error {
	data, err := codec.Encode(f)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

func writeErrorFrame(ctx context.Context, conn *websocket.Conn, codec protocol.Codec, code, msg string) error {
	f := protocol.Frame{Type: protocol.TypeError}
	_ = protocol.EncodeData(&f, map[string]string{"code": code, "message": msg})
	return writeFrame(ctx, conn, codec, f)
}
