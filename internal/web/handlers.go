package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/texhik/conclave/internal/app"
	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/llm"
	"github.com/texhik/conclave/internal/questions"
	"github.com/texhik/conclave/internal/tracker"
	"github.com/texhik/conclave/internal/usage"
)

type postReq struct {
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Value     string          `json:"value"`
	Mode      string          `json:"mode"`
	Model     string          `json:"model"`
	Effort    string          `json:"effort"`
	TaskID    string          `json:"task_id"`
	Status    string          `json:"status"`
	Body      string          `json:"body"`
	Action    string          `json:"action"`
	Path      string          `json:"path"`
	Create    bool            `json:"create"`
	Provider  string          `json:"provider"`
	Profile   string          `json:"profile"`
	BaseURL   string          `json:"base_url"`
	APIKey    string          `json:"api_key"`
	Task      taskInput       `json:"task"`
	PlanOnly  bool            `json:"plan_only"`
	SessionID string          `json:"session_id"`
	Index     int             `json:"index"`
	SetID     string          `json:"set_id"`
	Answers   json.RawMessage `json:"answers"`
}

type taskInput struct {
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Context          string   `json:"context"`
	Complexity       int      `json:"complexity"`
	Priority         int      `json:"priority"`
	Status           string   `json:"status"`
	Component        string   `json:"component"`
	Labels           []string `json:"labels"`
	Epic             string   `json:"epic"`
	DependsOn        []string `json:"depends_on"`
	WriteScope       []string `json:"write_scope"`
	Acceptance       []string `json:"acceptance"`
	Requirements     []string `json:"requirements"`
	Constraints      []string `json:"constraints"`
	Verification     []string `json:"verification"`
	OutOfScope       []string `json:"out_of_scope"`
	ComplexityReason string   `json:"complexity_reason"`
	Kind             string   `json:"kind"`
	ParentID         string   `json:"parent_id"`
}

func (s *Server) openWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path   string `json:"path"`
		Create bool   `json:"create"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	path, err := filepath.Abs(strings.TrimSpace(req.Path))
	if err != nil || strings.TrimSpace(req.Path) == "" {
		httpError(w, fmt.Errorf("workspace path is required"), http.StatusBadRequest)
		return
	}
	if req.Create {
		if err := os.MkdirAll(path, 0o755); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
	} else if st, err := os.Stat(path); err != nil || !st.IsDir() {
		httpError(w, fmt.Errorf("workspace does not exist"), http.StatusBadRequest)
		return
	}
	if _, err := config.EnsureSeed(path); err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	ws, err := s.open(r.Context(), path)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) open(ctx context.Context, path string) (*workspace, error) {
	s.mu.Lock()
	for _, ws := range s.workspaces {
		if ws.Path == path {
			s.mu.Unlock()
			return ws, nil
		}
	}
	s.mu.Unlock()

	// app.New can perform disk and MCP setup. Do not hold the server lock while
	// opening one workspace, otherwise other workspaces cannot be served.
	a, err := app.New(ctx, path, "")
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(path))
	ws := &workspace{ID: "ws_" + hex.EncodeToString(sum[:8]), Path: path, App: a, agents: map[string]*AgentInfo{}}

	s.mu.Lock()
	for _, existing := range s.workspaces {
		if existing.Path == path {
			s.mu.Unlock()
			_ = a.Close()
			return existing, nil
		}
	}
	s.workspaces[ws.ID] = ws
	s.mu.Unlock()

	p := a.Tracker().Project()
	s.registry.register(path, p.Name, p.Key)
	go func() {
		ch, unsub := a.Bus().Subscribe()
		defer unsub()
		for ev := range ch {
			ev.WorkspaceID = ws.ID
			s.trackAgent(ws, ev)
			s.global.Publish(ev)
		}
	}()
	return ws, nil
}

func (s *Server) get(id string) *workspace { s.mu.Lock(); defer s.mu.Unlock(); return s.workspaces[id] }

func (s *Server) statePayload(ws *workspace) map[string]any {
	st := ws.App.State()
	var models []string
	for _, p := range ws.App.Providers() {
		for _, m := range ws.App.ProviderModels(p) {
			models = append(models, p+"/"+m)
		}
	}
	sort.Strings(models)
	project := ws.App.Tracker().Project()
	sessions, _ := ws.App.Sessions(context.Background())
	open, _ := ws.App.Tracker().CountOpen(context.Background())
	qcount, _ := ws.App.Questions().Count(context.Background())
	queueStatus := ws.App.Queue().Status()
	agents := queueStatus.Active
	if st.Busy && agents == 0 {
		agents = 1
	}
	p := map[string]any{
		"counters":  map[string]any{"board": open, "questions": qcount, "agents": agents},
		"workspace": map[string]any{"id": ws.ID, "path": ws.Path, "project": project},
		"state":     st, "models": models, "providers": ws.App.Providers(),
		"sessions": sessions, "profile": ws.App.ProfileConfig(),
		"profile_name": ws.App.ProfileName(), "profiles": ws.App.ProfileNames(),
	}
	if st.Session.Mode == domain.ModeConclave {
		p["conclave"] = ws.App.Conclave().Snapshot()
	}
	return p
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) postWorkspace(w http.ResponseWriter, r *http.Request) (*workspace, postReq, bool) {
	var req postReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return nil, req, false
	}
	ws := s.get(req.ID)
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return nil, req, false
	}
	return ws, req, true
}

func (s *Server) runWorkspaceTask(ws *workspace, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	ws.mu.Lock()
	if ws.cancel != nil {
		ws.cancel()
	}
	ws.cancel = cancel
	ws.mu.Unlock()
	go func() {
		started := time.Now()
		defer func() { cancel(); ws.mu.Lock(); ws.cancel = nil; ws.mu.Unlock() }()
		if err := fn(ctx); err != nil {
			s.logf("workspace task error: workspace=%s duration=%s error=%v", ws.Path, time.Since(started).Round(time.Millisecond), err)
		}
	}()
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		httpError(w, fmt.Errorf("message is empty"), http.StatusBadRequest)
		return
	}
	st := ws.App.State()
	if st.Busy {
		httpError(w, fmt.Errorf("agent is busy"), http.StatusConflict)
		return
	}
	s.logf("message start: workspace=%s session=%s model=%s chars=%d", ws.Path, st.Session.ID, st.Session.Model, len(req.Text))
	s.runWorkspaceTask(ws, func(ctx context.Context) error { return ws.App.Send(ctx, req.Text) })
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.Revert(r.Context(), req.Index); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.Fork(r.Context(), req.Index); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) regenerate(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if ws.App.State().Busy {
		httpError(w, fmt.Errorf("agent is busy"), http.StatusConflict)
		return
	}
	s.runWorkspaceTask(ws, func(ctx context.Context) error { return ws.App.RegenerateAt(ctx, req.Index) })
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) continueChat(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if ws.App.State().Busy {
		httpError(w, fmt.Errorf("agent is busy"), http.StatusConflict)
		return
	}
	s.runWorkspaceTask(ws, func(ctx context.Context) error { return ws.App.Continue(ctx) })
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	ws.mu.Lock()
	if ws.cancel != nil {
		ws.cancel()
		ws.cancel = nil
	}
	ws.mu.Unlock()
	if ws.App.Conclave().Running() {
		_ = ws.App.Conclave().Pause()
		s.logf("conclave paused by cancel: workspace=%s", ws.Path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) mode(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	mode := domain.Mode(req.Mode)
	planAlias := req.Mode == "plan"
	if planAlias {
		mode = domain.ModeBuild
	}
	if err := ws.App.SetMode(mode); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if planAlias {
		_ = ws.App.SetPlanOnly(true)
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.SetProfile(req.Profile); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) model(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	ref := req.Model
	if req.Provider != "" && req.Value != "" {
		if err := ws.App.EnsureModel(req.Provider, req.Value, 128000, 8192); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		ref = req.Provider + "/" + req.Value
	}
	if err := ws.App.SetModel(ref); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) effort(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.SetEffort(req.Effort); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) planOnly(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.SetPlanOnly(req.PlanOnly); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.NewSession(); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.OpenSession(r.Context(), req.SessionID); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.DeleteSession(r.Context(), req.SessionID); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) compact(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := ws.App.Compact(ctx); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) conclaveAction(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	var err error
	switch req.Action {
	case "approve":
		err = ws.App.Conclave().Approve(context.Background())
	case "pause":
		err = ws.App.Conclave().Pause()
	case "resume":
		err = ws.App.Conclave().Resume(context.Background())
	case "cancel":
		err = ws.App.Conclave().Cancel()
	case "retry_review":
		err = ws.App.Conclave().RetryReview(context.Background())
	default:
		err = fmt.Errorf("unknown action %q", req.Action)
	}
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		ws := s.get(r.URL.Query().Get("id"))
		if ws == nil {
			httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
			return
		}
		type info struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		}
		var out []info
		for _, p := range ws.App.Providers() {
			out = append(out, info{Name: p, Models: ws.App.ProviderModels(p)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": out})
		return
	}
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.AddProvider(req.Provider, req.BaseURL, req.APIKey); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) providerModels(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	models, err := ws.App.FetchModels(ctx, req.Provider)
	if err != nil {
		httpError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) providerModel(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	if err := ws.App.EnsureModel(req.Provider, req.Value, 128000, 8192); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload(ws))
}

func (s *Server) configGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadGlobalOnly()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	providers := map[string]any{}
	for name, p := range cfg.Providers {
		models := map[string]any{}
		for id, m := range p.Models {
			models[id] = map[string]any{"context": m.Context, "max_output": m.MaxOutput, "tools": m.Capabilities.Tools, "reasoning": m.Capabilities.Reasoning, "effort": m.Capabilities.Effort}
		}
		providers[name] = map[string]any{"name": name, "base_url": p.BaseURL, "max_parallel": p.MaxParallel, "has_key": p.APIKey != "", "masked_key": config.MaskKey(p.APIKey), "models": models}
	}
	profiles := map[string]config.Profile{}
	for name := range cfg.Profiles {
		if p, err := cfg.Profile(name); err == nil {
			profiles[name] = p
		}
	}
	mcpServers := map[string]any{}
	for name, srv := range cfg.MCP {
		enabled := true
		if srv.Enabled != nil {
			enabled = *srv.Enabled
		}
		mcpServers[name] = map[string]any{
			"name": name, "type": srv.Type, "url": srv.URL, "command": srv.Command,
			"headers": srv.Headers, "environment": srv.Env, "enabled": enabled,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": providers, "default_profile": cfg.DefaultProfile, "profiles": profiles,
		"mcp": mcpServers,
		"search": map[string]any{
			"provider": cfg.Search.Provider, "base_url": cfg.Search.BaseURL,
			"max_results": cfg.Search.MaxResults, "has_key": cfg.Search.APIKey != "",
			"masked_key": config.MaskKey(cfg.Search.APIKey),
		},
		"server": map[string]any{"address": cfg.Server.Address},
	})
}

func (s *Server) configDefaultProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if err := config.SetGlobalDefaultProfile(req.Profile); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if err := config.SetGlobalServerAddress(req.Address); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warnings": []string{"listen address changes require restarting conclave serve"}})
}

func (s *Server) configMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := config.DeleteGlobalMCP(r.URL.Query().Get("name")); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.reloadResult())
		return
	}
	var req struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Command []string          `json:"command"`
		Headers map[string]string `json:"headers"`
		Env     map[string]string `json:"environment"`
		Enabled *bool             `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if err := config.UpsertGlobalMCP(req.Name, config.MCPServer{
		Type: req.Type, URL: req.URL, Command: req.Command, Headers: req.Headers, Env: req.Env, Enabled: req.Enabled,
	}); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider   string `json:"provider"`
		APIKey     string `json:"api_key"`
		BaseURL    string `json:"base_url"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if err := config.SetGlobalSearch(config.SearchConfig{
		Provider: req.Provider, APIKey: req.APIKey, BaseURL: req.BaseURL, MaxResults: req.MaxResults,
	}); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := config.DeleteGlobalProvider(r.URL.Query().Get("name")); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.reloadResult())
		return
	}
	var req struct {
		Name        string `json:"name"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		MaxParallel int    `json:"max_parallel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if req.MaxParallel <= 0 {
		req.MaxParallel = 8
	}
	if err := config.UpsertGlobalProvider(req.Name, config.Provider{BaseURL: req.BaseURL, APIKey: req.APIKey, MaxParallel: req.MaxParallel}); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if err := config.SetGlobalKey(req.Provider, req.APIKey); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configModel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := config.DeleteGlobalModel(r.URL.Query().Get("provider"), r.URL.Query().Get("model")); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.reloadResult())
		return
	}
	var req struct {
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Context   int    `json:"context"`
		MaxOutput int    `json:"max_output"`
		Tools     bool   `json:"tools"`
		Reasoning bool   `json:"reasoning"`
		Effort    bool   `json:"effort_cap"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	if req.Context <= 0 {
		req.Context = 128000
	}
	if req.MaxOutput <= 0 {
		req.MaxOutput = 8192
	}
	if err := config.UpsertGlobalModel(req.Provider, req.Model, config.Model{Context: req.Context, MaxOutput: req.MaxOutput, Capabilities: config.Capabilities{Tools: req.Tools, Reasoning: req.Reasoning, Effort: req.Effort}}); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := config.DeleteGlobalProfile(r.URL.Query().Get("name")); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.reloadResult())
		return
	}
	var req struct {
		Name    string         `json:"name"`
		Profile config.Profile `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	cfg, err := config.LoadGlobalOnly()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	refs := []string{req.Profile.Agent.Model, req.Profile.Master.Model}
	for _, w := range req.Profile.Workers {
		refs = append(refs, w.Model)
	}
	for _, ref := range refs {
		if ref != "" {
			if _, err := cfg.ResolveModel(ref); err != nil {
				httpError(w, fmt.Errorf("unknown model %q in profile", ref), http.StatusBadRequest)
				return
			}
		}
	}
	if err := config.UpsertGlobalProfile(req.Name, req.Profile); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.reloadResult())
}

func (s *Server) configTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	cfg, err := config.LoadGlobalOnly()
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	p, ok := cfg.Providers[req.Provider]
	if !ok {
		httpError(w, fmt.Errorf("provider %q not found", req.Provider), http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	models, err := llm.ListModels(ctx, p)
	if err != nil {
		httpError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) reloadAll() []string {
	s.mu.Lock()
	list := make([]*workspace, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		list = append(list, ws)
	}
	s.mu.Unlock()
	var warnings []string
	for _, ws := range list {
		if err := ws.App.ReloadConfig(); err != nil {
			msg := fmt.Sprintf("%s: %v", ws.Path, err)
			warnings = append(warnings, msg)
			s.logf("config reload failed: workspace=%s error=%v", ws.Path, err)
		} else {
			s.logf("config reloaded: workspace=%s", ws.Path)
		}
	}
	return warnings
}

func (s *Server) reloadResult() map[string]any {
	return map[string]any{"ok": true, "warnings": s.reloadAll()}
}

func (s *Server) questionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		ws := s.get(r.URL.Query().Get("id"))
		if ws == nil {
			httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
			return
		}
		if err := ws.App.Questions().Delete(r.Context(), r.URL.Query().Get("set_id")); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	sets, err := ws.App.Questions().List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if sets == nil {
		sets = []questions.Set{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": sets})
}

func (s *Server) questionsAnswer(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	answers := map[string]questions.Question{}
	if err := json.Unmarshal(req.Answers, &answers); err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	set, err := ws.App.Questions().Answer(r.Context(), req.SetID, answers, "human")
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) usageView(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	if taskID := r.URL.Query().Get("task"); taskID != "" {
		models, err := ws.App.Usage().ByTask(r.Context(), taskID)
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
		return
	}
	if r.URL.Query().Get("group") == "sessions" {
		sessions, err := ws.App.Usage().BySession(r.Context())
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		list, _ := ws.App.Sessions(r.Context())
		titles := map[string]string{}
		for _, item := range list {
			titles[item.ID] = item.Title
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "titles": titles})
		return
	}
	models, err := ws.App.Usage().ByModel(r.Context())
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if models == nil {
		models = []usage.ModelAgg{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	tasks, err := ws.App.Tracker().List(r.Context(), tracker.Filter{Limit: 500})
	if tasks == nil {
		tasks = []tracker.Task{}
	}
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": ws.App.Tracker().Project(), "tasks": tasks, "queue": ws.App.Queue().Status()})
}

func (s *Server) boardTask(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		ws := s.get(r.URL.Query().Get("id"))
		if ws == nil {
			httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
			return
		}
		if err := ws.App.Tracker().Delete(r.Context(), r.URL.Query().Get("task")); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	in := tracker.CreateInput{Title: req.Task.Title, Description: req.Task.Description, Context: req.Task.Context, Complexity: req.Task.Complexity, Priority: req.Task.Priority, Status: tracker.TaskStatus(req.Task.Status), Component: req.Task.Component, Labels: req.Task.Labels, Epic: req.Task.Epic, DependsOn: req.Task.DependsOn, WriteScope: req.Task.WriteScope, Acceptance: req.Task.Acceptance, Requirements: req.Task.Requirements, Constraints: req.Task.Constraints, Verification: req.Task.Verification, OutOfScope: req.Task.OutOfScope, ComplexityReason: req.Task.ComplexityReason, Kind: req.Task.Kind, ParentID: req.Task.ParentID, CreatedBy: "user", OriginSessionID: ws.App.ActiveSessionID()}
	var task *tracker.Task
	var err error
	if req.TaskID == "" {
		task, err = ws.App.Tracker().Create(r.Context(), in, "user")
	} else {
		task, err = ws.App.Tracker().Update(r.Context(), req.TaskID, in, "user")
	}
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) boardTransition(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	task, err := ws.App.Tracker().Transition(r.Context(), req.TaskID, tracker.TaskStatus(req.Status), "user")
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) boardComment(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	c, err := ws.App.Tracker().AddComment(r.Context(), req.TaskID, "user", req.Body)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) boardTaskDetail(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	task, err := ws.App.Tracker().Get(r.Context(), r.URL.Query().Get("task"))
	if err != nil {
		httpError(w, err, http.StatusNotFound)
		return
	}
	comments, _ := ws.App.Tracker().Comments(r.Context(), task.ID)
	events, _ := ws.App.Tracker().Events(r.Context(), task.ID)
	attempts, _ := ws.App.Tracker().Attempts(r.Context(), task.ID)
	results, _ := ws.App.Tracker().Results(r.Context(), task.ID)
	if attempts == nil {
		attempts = []tracker.Attempt{}
	}
	if results == nil {
		results = []tracker.Result{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task, "comments": comments, "events": events, "attempts": attempts, "results": results})
}

func (s *Server) boardTaskLogs(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	logs, err := ws.App.Tracker().Logs(r.Context(), r.URL.Query().Get("task"), r.URL.Query().Get("attempt"))
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	if logs == nil {
		logs = []tracker.LogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (s *Server) boardQueue(w http.ResponseWriter, r *http.Request) {
	ws, req, ok := s.postWorkspace(w, r)
	if !ok {
		return
	}
	switch req.Action {
	case "start":
		var err error
		if snap := ws.App.Conclave().Snapshot(); snap.Run != nil && snap.Run.Phase == domain.RunPaused {
			err = ws.App.Conclave().Resume(context.Background())
		} else {
			err = ws.App.StartQueue(context.Background())
		}
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
	case "pause":
		ws.App.PauseQueue()
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": ws.App.Queue().Status()})
}

func (s *Server) globalWebsocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	events, unsub := s.global.Subscribe()
	defer unsub()
	ctx := conn.CloseRead(r.Context())
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			if err := conn.Write(context.Background(), websocket.MessageText, b); err != nil {
				return
			}
		}
	}
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	events, unsub := ws.App.Bus().Subscribe()
	defer unsub()
	ctx := conn.CloseRead(r.Context())
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			ev.WorkspaceID = ws.ID
			b, _ := json.Marshal(ev)
			if err := conn.Write(context.Background(), websocket.MessageText, b); err != nil {
				return
			}
		}
	}
}

func (s *Server) fsList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path, _ = os.UserHomeDir()
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	writeJSON(w, http.StatusOK, map[string]any{"path": abs, "parent": filepath.Dir(abs), "dirs": dirs})
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
		return
	}
	ident := r.URL.Query().Get("id")
	ws := s.get(ident)
	if ws == nil {
		s.mu.Lock()
		for _, candidate := range s.workspaces {
			if candidate.Path == ident {
				ws = candidate
				break
			}
		}
		s.mu.Unlock()
	}
	if ws == nil {
		s.registry.remove(ident)
		s.logf("workspace forgotten: %s", ident)
		writeJSON(w, http.StatusOK, map[string]any{"forgotten": true})
		return
	}
	if ws.App.State().Busy || ws.App.Queue().Running() || ws.App.Conclave().Running() {
		httpError(w, fmt.Errorf("workspace is active; stop agents and queue before forgetting it"), http.StatusConflict)
		return
	}
	s.mu.Lock()
	delete(s.workspaces, ws.ID)
	s.mu.Unlock()
	_ = ws.App.Close()
	s.registry.remove(ws.Path)
	s.logf("workspace forgotten: %s", ws.Path)
	writeJSON(w, http.StatusOK, map[string]any{"forgotten": true})
}

func (s *Server) workspacesList(w http.ResponseWriter, r *http.Request) {
	records := s.registry.list()
	s.mu.Lock()
	byPath := map[string]*workspace{}
	for _, ws := range s.workspaces {
		byPath[ws.Path] = ws
	}
	s.mu.Unlock()
	out := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		item := map[string]any{"path": rec.Path, "name": rec.Name, "key": rec.Key, "last_opened": rec.LastOpened, "exists": rec.Exists, "active": false, "state": "idle", "running_agents": 0, "open_tasks": 0, "pending_questions": 0}
		if ws := byPath[rec.Path]; ws != nil {
			st := ws.App.State()
			open, _ := ws.App.Tracker().CountOpen(context.Background())
			q, _ := ws.App.Questions().Count(context.Background())
			queue := ws.App.Queue().Status()
			active := st.Busy || queue.Running || queue.Active > 0 || (ws.App.Conclave() != nil && ws.App.Conclave().Running())
			state := "idle"
			if active {
				state = "active"
			} else if q > 0 {
				state = "waiting"
			}
			agents := queue.Active
			if st.Busy && agents == 0 {
				agents = 1
			}
			item["id"] = ws.ID
			item["active"], item["state"], item["running_agents"], item["open_tasks"], item["pending_questions"] = active, state, agents, open, q
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		rank := func(x map[string]any) int {
			if x["active"] == true {
				return 0
			}
			if x["state"] == "waiting" {
				return 1
			}
			return 2
		}
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		ti, _ := out[i]["last_opened"].(time.Time)
		tj, _ := out[j]["last_opened"].(time.Time)
		return ti.After(tj)
	})
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": out})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) logsHandler(w http.ResponseWriter, r *http.Request) {
	s.logMu.Lock()
	out := append([]string(nil), s.logs...)
	s.logMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, err error, status int) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
func execCommand(name string, args ...string) error { return exec.Command(name, args...).Run() }

func spaHandler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

var _ = app.App{}
