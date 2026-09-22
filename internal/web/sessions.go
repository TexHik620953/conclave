package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/store"
)

func newSessionID() string {
	return orchestrator.NewRunID("sess")
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	sessions, err := s.deps.ListSessions(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []store.Session{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Pipeline  string `json:"pipeline"`
		Workspace string `json:"workspace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	id := body.ID
	if id == "" {
		id = newSessionID()
	}
	if err := store.ValidateID(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	sess := store.Session{
		ID: id, Title: body.Title, Pipeline: body.Pipeline, Workspace: body.Workspace,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.deps.CreateSession(sess); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.deps.GetSession(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	messages, _ := s.deps.ListMessages(id, 2000)
	runs, _ := s.deps.ListRunsBySession(id, 200)
	if messages == nil {
		messages = []store.Message{}
	}
	if runs == nil {
		runs = []store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sess,
		"messages": messages,
		"runs":     runs,
	})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.DeleteSession(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := 2000
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	messages, err := s.deps.ListMessages(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if messages == nil {
		messages = []store.Message{}
	}
	writeJSON(w, http.StatusOK, messages)
}

// handlePostMessage appends a user message to a session and either starts a new
// run or queues the message for the active run.
func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.deps.GetSession(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var body struct {
		Content   string `json:"content"`
		Pipeline  string `json:"pipeline"`
		Workspace string `json:"workspace"`
		RunID     string `json:"run_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Deliver to the active run if there is one; otherwise start a new run.
	if activeID := s.runs.activeForSession(id); activeID != "" {
		msg := store.Message{
			SessionID: id, RunID: activeID, Role: "user", Kind: "user", Content: content,
		}
		if _, err := s.deps.AppendMessage(msg); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.deps.EmitEvent(event.Event{
			Type: "user.message", RunID: activeID, Role: "user", Message: content,
		})
		s.inbox.push(activeID, content)
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": activeID, "queued": "true"})
		return
	}

	pipeline := body.Pipeline
	if pipeline == "" {
		pipeline = sess.Pipeline
	}
	pipeline = s.defaultPipeline(pipeline)
	workspace := body.Workspace
	if workspace == "" {
		workspace = sess.Workspace
	}
	runID := body.RunID
	if runID == "" {
		runID = newRunID(pipeline)
	}
	if err := store.ValidateID(runID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	if _, err := s.deps.AppendMessage(store.Message{
		SessionID: id, RunID: runID, Role: "user", Kind: "user", Content: content,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sess.Title == "" {
		_ = s.deps.TouchSessionTitle(id, firstLine(content, 80))
	}
	s.launchSession(id, runID, orchestrator.RunOptions{
		Pipeline: pipeline, SessionID: id, Task: content, Workspace: workspace, RunID: runID,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}
