// Package api serves the local agent Web UI and a small API that proxies
// conclave-core and forwards its live events over SSE.
package api

import (
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/texhik/conclave/conclave-agent/internal/config"
	"github.com/texhik/conclave/conclave-agent/internal/conn"
	"github.com/texhik/conclave/conclave-agent/internal/corehttp"
	"github.com/texhik/conclave/conclave-agent/internal/hub"
	"github.com/texhik/conclave/conclave-agent/internal/tools"
)

//go:embed ui/index.html
var uiFS embed.FS

// Deps are the agent services the API needs.
type Deps struct {
	Config config.Config
	Conn   *conn.Conn
	Hub    *hub.Hub
	Core   *corehttp.Client
	Tools  *tools.Registry
	Logger *slog.Logger
}

// Server serves the UI and API.
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New creates a server.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{deps: deps}
	s.routes()
	return s
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /", s.handleIndex)
	m.HandleFunc("GET /api/status", s.handleStatus)
	m.HandleFunc("GET /api/events", s.handleEvents)
	m.HandleFunc("GET /api/tools", s.handleTools)
	m.HandleFunc("POST /api/sessions/{id}/open", s.handleOpen)

	m.HandleFunc("GET /api/sessions", s.proxyUser(http.MethodGet, "/v1/sessions"))
	m.HandleFunc("POST /api/sessions", s.proxyUser(http.MethodPost, "/v1/sessions"))
	m.HandleFunc("GET /api/sessions/{id}", s.proxyUserPath(http.MethodGet, "/v1/sessions/%s"))
	m.HandleFunc("GET /api/sessions/{id}/timeline", s.proxyUserPath(http.MethodGet, "/v1/sessions/%s/timeline"))
	m.HandleFunc("GET /api/sessions/{id}/messages", s.proxyUserPath(http.MethodGet, "/v1/sessions/%s/messages"))
	m.HandleFunc("GET /api/sessions/{id}/plan", s.proxyUserPath(http.MethodGet, "/v1/sessions/%s/plan"))
	m.HandleFunc("GET /api/sessions/{id}/questions", s.proxyUserPath(http.MethodGet, "/v1/sessions/%s/questions"))
	m.HandleFunc("GET /api/sessions/{id}/jobs", s.proxyUserPath(http.MethodGet, "/v1/sessions/%s/jobs"))
	m.HandleFunc("POST /api/sessions/{id}/interview", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/interview"))
	m.HandleFunc("POST /api/sessions/{id}/message", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/message"))
	m.HandleFunc("POST /api/sessions/{id}/answers", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/answers"))
	m.HandleFunc("POST /api/sessions/{id}/plans", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/plans"))
	m.HandleFunc("POST /api/sessions/{id}/runs", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/runs"))
	m.HandleFunc("POST /api/sessions/{id}/interrupt", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/interrupt"))
	m.HandleFunc("POST /api/sessions/{id}/cancel", s.proxyUserPath(http.MethodPost, "/v1/sessions/%s/cancel"))
	m.HandleFunc("DELETE /api/sessions/{id}", s.proxyUserPath(http.MethodDelete, "/v1/sessions/%s"))
	m.HandleFunc("POST /api/questions/{id}/answer", s.proxyUserPath(http.MethodPost, "/v1/questions/%s/answer"))
	s.mux = m
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "ui not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	_, _ = w.Write(data)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":        s.deps.Conn.DeviceID(),
		"agent_connected":  s.deps.Conn.AgentConnected(),
		"client_connected": s.deps.Conn.ClientConnected(),
		"workspace":        s.deps.Config.Workspace,
		"core_url":         s.deps.Config.CoreURL,
		"tools":            s.deps.Tools.Names(),
	})
}

func (s *Server) handleTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Tools.Definitions())
}

// handleEvents streams core events to the browser as SSE.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.deps.Hub.Subscribe(2048)
	defer cancel()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleOpen binds the session to this device and starts streaming its events.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.Conn.Subscribe(id); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.deps.Conn.OpenSession(r.Context(), id); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"opened": true})
}

// --- core proxy ---

func (s *Server) proxyUser(method, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.forward(w, r, method, path) }
}

func (s *Server) proxyUserPath(method, format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.forward(w, r, method, sprintf(format, r.PathValue("id")))
	}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, method, path string) {
	var body any
	if r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if len(raw) > 0 {
			body = json.RawMessage(raw)
		}
	}
	data, status, err := s.deps.Core.Do(r.Context(), method, path, body, corehttp.AuthDevice)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func sprintf(format, a string) string {
	out := make([]byte, 0, len(format)+len(a))
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) && format[i+1] == 's' {
			out = append(out, a...)
			i++
			continue
		}
		out = append(out, format[i])
	}
	return string(out)
}
