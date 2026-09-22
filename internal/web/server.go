package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/store"
)

// maxBodyBytes caps request bodies to avoid memory-exhaustion DoS.
const maxBodyBytes = 4 << 20

//go:embed all:dist
var distFS embed.FS

// Deps is the application surface the web server needs.
type Deps interface {
	Config() *config.Config
	ListRuns(limit int) ([]store.Run, error)
	GetRun(id string) (*store.Run, error)
	ListNodes(id string) ([]store.NodeRecord, error)
	ListArtifacts(id string) ([]store.Artifact, error)
	ListEvents(id string, limit int) ([]store.EventRecord, error)
	ListRecentEvents(id string, limit int) ([]store.EventRecord, error)
	ArtifactContent(runID, name string) (string, error)
	SetRunStatus(id, status string) error
	RevertFrom(runID, nodeID string) error
	Bus() *event.Bus
	RunPipeline(ctx context.Context, opts orchestrator.RunOptions) (*orchestrator.RunResult, error)
	AskRole(ctx context.Context, runID, roleID, prompt, contextText, workspace string) error

	// Sessions and chat messages.
	CreateSession(store.Session) error
	GetSession(id string) (*store.Session, error)
	ListSessions(limit int) ([]store.Session, error)
	DeleteSession(id string) error
	TouchSessionTitle(id, title string) error
	ListMessages(sessionID string, limit int) ([]store.Message, error)
	ListRunsBySession(sessionID string, limit int) ([]store.Run, error)
	AppendMessage(store.Message) (int64, error)
	EmitEvent(event.Event)
}

// Options configures the server.
type Options struct {
	Token string
	Log   *slog.Logger
	// AllowedOrigins are extra WebSocket origin host patterns. Nil uses the
	// same-host default plus localhost (for the Vite dev server).
	AllowedOrigins []string
}

// Server serves the REST API, WebSocket and the embedded frontend.
type Server struct {
	deps   Deps
	hub    *Hub
	broker *Broker
	token  string
	log    *slog.Logger
	ctx    context.Context
	runs   *runManager
	inbox  *inboxManager
	mux    *http.ServeMux
}

// New creates a server. The context bounds all runs started by the server.
func New(ctx context.Context, deps Deps, opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		deps:   deps,
		hub:    NewHub(deps.Bus(), opts.AllowedOrigins),
		broker: NewBroker(deps.Bus()),
		token:  opts.Token,
		log:    log,
		ctx:    ctx,
		runs:   newRunManager(),
		inbox:  newInboxManager(),
	}
	s.routes()
	return s
}

// Broker exposes the question broker so the engine can use it as its Asker.
func (s *Server) Broker() *Broker { return s.broker }

// Inbox drains user messages queued for an active run.
func (s *Server) Inbox(runID string) []string { return s.inbox.drain(runID) }

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/config", s.handleConfig)
	m.HandleFunc("GET /api/runs", s.handleListRuns)
	m.HandleFunc("POST /api/runs", s.handleCreateRun)
	m.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	m.HandleFunc("POST /api/runs/{id}/cancel", s.handleCancelRun)
	m.HandleFunc("POST /api/runs/{id}/pause", s.handlePauseRun)
	m.HandleFunc("POST /api/runs/{id}/resume", s.handleResumeRun)
	m.HandleFunc("POST /api/runs/{id}/revert", s.handleRevertRun)
	m.HandleFunc("POST /api/runs/{id}/followup", s.handleFollowupRun)
	m.HandleFunc("POST /api/runs/{id}/answer", s.handleAnswer)
	m.HandleFunc("GET /api/runs/{id}/artifacts/{name}", s.handleArtifact)
	m.HandleFunc("POST /api/ask", s.handleAsk)
	m.HandleFunc("GET /api/sessions", s.handleListSessions)
	m.HandleFunc("POST /api/sessions", s.handleCreateSession)
	m.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	m.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	m.HandleFunc("GET /api/sessions/{id}/messages", s.handleListMessages)
	m.HandleFunc("POST /api/sessions/{id}/messages", s.handlePostMessage)
	m.HandleFunc("/api/ws", s.hub.Serve)
	m.Handle("/", s.spaHandler())
	s.mux = m
}

// Handler returns the root HTTP handler with security headers, body limits and
// auth applied.
func (s *Server) Handler() http.Handler {
	return s.security(s.limitBody(s.auth(s.mux)))
}

// security adds conservative response headers.
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// limitBody caps the size of request bodies.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe runs the server until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: WebSocket and streaming responses are long-lived.
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	s.log.Info("conclave web", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		} else {
			// Browsers cannot set headers on a WebSocket handshake, so allow the
			// token as a query parameter there.
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves the embedded frontend, falling back to index.html.
func (s *Server) spaHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			serveIndex(w, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

type runManager struct {
	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
	paused    map[string]bool
	sessions  map[string]string // runID -> sessionID
	bySession map[string]string // sessionID -> active runID
}

func newRunManager() *runManager {
	return &runManager{
		cancels:   map[string]context.CancelFunc{},
		paused:    map[string]bool{},
		sessions:  map[string]string{},
		bySession: map[string]string{},
	}
}

func (rm *runManager) add(id string, cancel context.CancelFunc) {
	rm.mu.Lock()
	rm.cancels[id] = cancel
	rm.mu.Unlock()
}

// addSession registers a run and associates it with a chat session.
func (rm *runManager) addSession(id, sessionID string, cancel context.CancelFunc) {
	rm.addSessionIfIdle(id, sessionID, cancel, false)
}

// addSessionIfIdle atomically registers a run. When exclusive is true it fails
// if the run is already active, closing the resume/follow-up check-then-launch
// race.
func (rm *runManager) addSessionIfIdle(id, sessionID string, cancel context.CancelFunc, exclusive bool) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if exclusive {
		if _, ok := rm.cancels[id]; ok {
			return false
		}
	}
	rm.cancels[id] = cancel
	if sessionID != "" {
		rm.sessions[id] = sessionID
		rm.bySession[sessionID] = id
	}
	return true
}

// activeForSession returns the active run id for a session, if any.
func (rm *runManager) activeForSession(sessionID string) string {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	id := rm.bySession[sessionID]
	if id == "" {
		return ""
	}
	if _, ok := rm.cancels[id]; !ok {
		delete(rm.bySession, sessionID)
		return ""
	}
	return id
}

func (rm *runManager) remove(id string) {
	rm.mu.Lock()
	delete(rm.cancels, id)
	if sid := rm.sessions[id]; sid != "" {
		delete(rm.sessions, id)
		if rm.bySession[sid] == id {
			delete(rm.bySession, sid)
		}
	}
	rm.mu.Unlock()
}

func (rm *runManager) cancel(id string) bool {
	rm.mu.Lock()
	cancel, ok := rm.cancels[id]
	rm.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// requestPause marks the run as pause-requested and cancels its context.
func (rm *runManager) requestPause(id string) bool {
	rm.mu.Lock()
	cancel, ok := rm.cancels[id]
	if ok {
		rm.paused[id] = true
	}
	rm.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// wasPaused reports whether a pause was requested for the run.
func (rm *runManager) wasPaused(id string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.paused[id]
}

func (rm *runManager) clearPaused(id string) {
	rm.mu.Lock()
	delete(rm.paused, id)
	rm.mu.Unlock()
}

func (rm *runManager) active(id string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	_, ok := rm.cancels[id]
	return ok
}

func newRunID(pipeline string) string {
	return orchestrator.NewRunID(pipeline)
}
