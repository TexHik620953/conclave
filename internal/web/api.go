package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tool"
)

// defaultPipeline returns the requested pipeline or the configured default.
func (s *Server) defaultPipeline(name string) string {
	if name != "" {
		return name
	}
	if cfg := s.deps.Config(); cfg != nil && cfg.Settings.DefaultPipeline != "" {
		return cfg.Settings.DefaultPipeline
	}
	return "auto"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type pipelineDTO struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Inputs      []string  `json:"inputs"`
	Nodes       []nodeDTO `json:"nodes"`
}

type nodeDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Role string `json:"role,omitempty"`
}

type roleDTO struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Model       string          `json:"model"`
	Fallback    []string        `json:"fallback,omitempty"`
	Tools       []string        `json:"tools,omitempty"`
	Permissions map[string]bool `json:"permissions"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.Config()
	pipelines := make([]pipelineDTO, 0, len(cfg.Pipelines))
	for name, p := range cfg.Pipelines {
		nodes := make([]nodeDTO, 0, len(p.Nodes))
		for _, n := range p.Nodes {
			nodes = append(nodes, nodeDTO{ID: n.ID, Type: n.Type, Role: n.Role})
		}
		pipelines = append(pipelines, pipelineDTO{Name: name, Description: p.Description, Inputs: p.Inputs, Nodes: nodes})
	}
	roles := make([]roleDTO, 0, len(cfg.Roles))
	for id, role := range cfg.Roles {
		perms := map[string]bool{
			"fs_read":  role.Permissions.Allowed("fs_read", false),
			"fs_write": role.Permissions.Allowed("fs_write", false),
			"shell":    role.Permissions.Allowed("shell", false),
			"network":  role.Permissions.Allowed("network", false),
		}
		roles = append(roles, roleDTO{
			ID: id, Title: role.Title, Description: role.Description, Model: role.Model,
			Fallback: role.Fallback, Tools: role.Tools, Permissions: perms,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pipelines": pipelines,
		"roles":     roles,
		"settings": map[string]any{
			"workspace":      cfg.Settings.Workspace,
			"max_parallel":   cfg.Settings.MaxParallel,
			"max_iterations": cfg.Settings.MaxIterations,
		},
	})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	runs, err := s.deps.ListRuns(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []store.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pipeline    string            `json:"pipeline"`
		SessionID   string            `json:"session_id"`
		Task        string            `json:"task"`
		Workspace   string            `json:"workspace"`
		RunID       string            `json:"run_id"`
		ResumeRunID string            `json:"resume_run_id"`
		Inputs      map[string]string `json:"inputs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Workspace != "" {
		info, err := os.Stat(body.Workspace)
		if err != nil || !info.IsDir() {
			writeError(w, http.StatusBadRequest, "workspace must be an existing directory")
			return
		}
	}
	id := body.ResumeRunID
	if id == "" {
		id = body.RunID
	}
	if id != "" {
		if err := store.ValidateID(id); err != nil {
			writeError(w, http.StatusBadRequest, "invalid run id")
			return
		}
	} else {
		id = newRunID(s.defaultPipeline(body.Pipeline))
	}
	s.launch(id, orchestrator.RunOptions{
		Pipeline: body.Pipeline, SessionID: body.SessionID, Task: body.Task, Workspace: body.Workspace,
		Inputs: body.Inputs, RunID: id, ResumeRunID: body.ResumeRunID,
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": id})
}

// launch starts a run asynchronously and tracks it for cancel/pause.
func (s *Server) launch(id string, opts orchestrator.RunOptions) {
	s.launchSession(id, opts.SessionID, opts)
}

// launchSession starts a run asynchronously, associating it with a chat session.
func (s *Server) launchSession(id, sessionID string, opts orchestrator.RunOptions) {
	s.launchSessionIfIdle(id, sessionID, opts, false)
}

// launchSessionIfIdle starts a run. When exclusive is true it refuses to start
// if the run is already active, closing the check-then-launch race.
func (s *Server) launchSessionIfIdle(id, sessionID string, opts orchestrator.RunOptions, exclusive bool) bool {
	ctx, cancel := context.WithCancel(s.ctx)
	if !s.runs.addSessionIfIdle(id, sessionID, cancel, exclusive) {
		cancel()
		return false
	}
	deps := s.deps
	log := s.log
	inbox := s.inbox
	go func() {
		_, err := deps.RunPipeline(ctx, opts)
		paused := s.runs.wasPaused(id)
		s.runs.remove(id)
		s.runs.clearPaused(id)
		inbox.clear(id)
		if paused {
			_ = deps.SetRunStatus(id, "paused")
			deps.Bus().Publish(event.Event{Type: event.RunFinished, RunID: id, Message: "paused"})
			return
		}
		if err != nil {
			log.Info("run finished with error", "run_id", id, "error", err.Error())
		}
	}()
	return true
}

func (s *Server) handlePauseRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.runs.requestPause(id) {
		writeError(w, http.StatusConflict, "run is not active")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
}

func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.runs.active(id) {
		writeError(w, http.StatusConflict, "run is already active")
		return
	}
	run, err := s.deps.GetRun(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if !s.launchSessionIfIdle(id, run.SessionID, orchestrator.RunOptions{ResumeRunID: id, RunID: id, SessionID: run.SessionID}, true) {
		writeError(w, http.StatusConflict, "run is already active")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": id})
}

func (s *Server) handleRevertRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.runs.active(id) {
		writeError(w, http.StatusConflict, "pause or wait for the run before reverting")
		return
	}
	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NodeID == "" {
		writeError(w, http.StatusBadRequest, "node_id is required")
		return
	}
	if err := s.deps.RevertFrom(id, body.NodeID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.deps.SetRunStatus(id, "paused")
	s.deps.Bus().Publish(event.Event{Type: event.RunFinished, RunID: id, Message: "paused"})
	writeJSON(w, http.StatusOK, map[string]any{"reverted": body.NodeID})
}

// handleFollowupRun re-runs the pipeline with an extra instruction while
// keeping all previous outputs as context (a continuation, not a restart).
func (s *Server) handleFollowupRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.deps.GetRun(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if !s.launchSessionIfIdle(id, run.SessionID, orchestrator.RunOptions{
		ResumeRunID: id, RunID: id, SessionID: run.SessionID, Followup: body.Prompt,
	}, true) {
		writeError(w, http.StatusConflict, "run is active; use the session message endpoint to queue a message")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": id})
}

// handleAnswer delivers a user's answer to a pending ask_user question.
func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string   `json:"id"`
		Selected []string `json:"selected"`
		Custom   string   `json:"custom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	ok := s.broker.AnswerFor(r.PathValue("id"), body.ID, tool.Answer{Selected: body.Selected, Custom: body.Custom})
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": ok})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.deps.GetRun(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	nodes, _ := s.deps.ListNodes(id)
	arts, _ := s.deps.ListArtifacts(id)
	events, _ := s.deps.ListRecentEvents(id, 200)
	var todos []tool.Todo
	if raw, err := s.deps.ArtifactContent(id, "todos.json"); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &todos)
	}
	if nodes == nil {
		nodes = []store.NodeRecord{}
	}
	if arts == nil {
		arts = []store.Artifact{}
	}
	if events == nil {
		events = []store.EventRecord{}
	}
	if todos == nil {
		todos = []tool.Todo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":       run,
		"nodes":     nodes,
		"artifacts": arts,
		"events":    events,
		"todos":     todos,
		"active":    s.runs.active(id),
	})
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	content, err := s.deps.ArtifactContent(id, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(content))
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": s.runs.cancel(id)})
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role      string `json:"role"`
		Prompt    string `json:"prompt"`
		Context   string `json:"context"`
		Workspace string `json:"workspace"`
		RunID     string `json:"run_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Role == "" || body.Prompt == "" {
		writeError(w, http.StatusBadRequest, "role and prompt are required")
		return
	}
	id := body.RunID
	if id == "" {
		id = newRunID("ask-" + body.Role)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.runs.add(id, cancel)
	deps := s.deps
	go func() {
		defer s.runs.remove(id)
		defer cancel()
		_ = deps.AskRole(ctx, id, body.Role, body.Prompt, body.Context, body.Workspace)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": id})
}
