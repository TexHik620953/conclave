package web

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/app"
	"github.com/texhik/conclave/internal/event"
)

//go:embed all:dist
var embedded embed.FS

type Server struct {
	mu         sync.Mutex
	workspaces map[string]*workspace
	registry   *workspaceRegistry
	global     *event.Bus
	logMu      sync.Mutex
	logs       []string
}

type workspace struct {
	ID     string
	Path   string
	App    *app.App
	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewServer() *Server {
	return &Server{workspaces: map[string]*workspace{}, registry: newRegistry(), global: event.New(), logs: []string{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/logs", s.logsHandler)
	mux.HandleFunc("/api/fs", s.fsList)
	mux.HandleFunc("/api/workspace/open", s.openWorkspace)
	mux.HandleFunc("/api/workspace", s.deleteWorkspace)
	mux.HandleFunc("/api/workspaces", s.workspacesList)
	mux.HandleFunc("/api/state", s.state)
	mux.HandleFunc("/api/message", s.message)
	mux.HandleFunc("/api/chat/revert", s.revert)
	mux.HandleFunc("/api/chat/fork", s.fork)
	mux.HandleFunc("/api/chat/regenerate", s.regenerate)
	mux.HandleFunc("/api/chat/continue", s.continueChat)
	mux.HandleFunc("/api/cancel", s.cancel)
	mux.HandleFunc("/api/mode", s.mode)
	mux.HandleFunc("/api/model", s.model)
	mux.HandleFunc("/api/effort", s.effort)
	mux.HandleFunc("/api/plan-only", s.planOnly)
	mux.HandleFunc("/api/session/new", s.newSession)
	mux.HandleFunc("/api/sessions/open", s.openSession)
	mux.HandleFunc("/api/session/delete", s.deleteSession)
	mux.HandleFunc("/api/compact", s.compact)
	mux.HandleFunc("/api/conclave/action", s.conclaveAction)
	mux.HandleFunc("/api/config", s.configGet)
	mux.HandleFunc("/api/config/providers", s.configProvider)
	mux.HandleFunc("/api/config/key", s.configKey)
	mux.HandleFunc("/api/config/model", s.configModel)
	mux.HandleFunc("/api/config/profile", s.configProfile)
	mux.HandleFunc("/api/config/test", s.configTest)
	mux.HandleFunc("/api/providers", s.providers)
	mux.HandleFunc("/api/providers/models", s.providerModels)
	mux.HandleFunc("/api/providers/model", s.providerModel)
	mux.HandleFunc("/api/questions", s.questionsList)
	mux.HandleFunc("/api/questions/answer", s.questionsAnswer)
	mux.HandleFunc("/api/usage", s.usageView)
	mux.HandleFunc("/api/board", s.board)
	mux.HandleFunc("/api/board/task", s.boardTask)
	mux.HandleFunc("/api/board/transition", s.boardTransition)
	mux.HandleFunc("/api/board/comment", s.boardComment)
	mux.HandleFunc("/api/board/task/detail", s.boardTaskDetail)
	mux.HandleFunc("/api/board/task/logs", s.boardTaskLogs)
	mux.HandleFunc("/api/board/queue", s.boardQueue)
	mux.HandleFunc("/ws", s.websocket)
	mux.HandleFunc("/ws/global", s.globalWebsocket)
	mux.Handle("/", spaHandler())
	return mux
}

func (s *Server) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	log.Print(line)
	s.logMu.Lock()
	s.logs = append(s.logs, line)
	if len(s.logs) > 500 {
		s.logs = s.logs[len(s.logs)-500:]
	}
	s.logMu.Unlock()
}
