package web

import (
	"context"
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
	ArtifactContent(runID, name string) (string, error)
	Bus() *event.Bus
	RunPipeline(ctx context.Context, opts orchestrator.RunOptions) (*orchestrator.RunResult, error)
	AskRole(ctx context.Context, runID, roleID, prompt, contextText, workspace string) error
}

// Options configures the server.
type Options struct {
	Token string
	Log   *slog.Logger
}

// Server serves the REST API, WebSocket and the embedded frontend.
type Server struct {
	deps  Deps
	hub   *Hub
	token string
	log   *slog.Logger
	ctx   context.Context
	runs  *runManager
	mux   *http.ServeMux
}

// New creates a server. The context bounds all runs started by the server.
func New(ctx context.Context, deps Deps, opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		deps:  deps,
		hub:   NewHub(deps.Bus()),
		token: opts.Token,
		log:   log,
		ctx:   ctx,
		runs:  newRunManager(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/config", s.handleConfig)
	m.HandleFunc("GET /api/runs", s.handleListRuns)
	m.HandleFunc("POST /api/runs", s.handleCreateRun)
	m.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	m.HandleFunc("POST /api/runs/{id}/cancel", s.handleCancelRun)
	m.HandleFunc("GET /api/runs/{id}/artifacts/{name}", s.handleArtifact)
	m.HandleFunc("POST /api/ask", s.handleAsk)
	m.HandleFunc("/api/ws", s.hub.Serve)
	m.Handle("/", s.spaHandler())
	s.mux = m
}

// Handler returns the root HTTP handler with auth applied.
func (s *Server) Handler() http.Handler {
	return s.auth(s.mux)
}

// ListenAndServe runs the server until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
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
			token = r.URL.Query().Get("token")
		}
		if token != s.token {
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
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newRunManager() *runManager {
	return &runManager{cancels: map[string]context.CancelFunc{}}
}

func (rm *runManager) add(id string, cancel context.CancelFunc) {
	rm.mu.Lock()
	rm.cancels[id] = cancel
	rm.mu.Unlock()
}

func (rm *runManager) remove(id string) {
	rm.mu.Lock()
	delete(rm.cancels, id)
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

func (rm *runManager) active(id string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	_, ok := rm.cancels[id]
	return ok
}

func newRunID(pipeline string) string {
	return orchestrator.NewRunID(pipeline)
}
