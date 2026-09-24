// Package api exposes the HTTP control plane and the WebSocket endpoint for
// local agents.
package api

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/auth"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/obs"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/session"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Deps are the application services the API needs.
type Deps struct {
	Store          store.Store
	Events         *events.Log
	Auth           *auth.Manager
	Sessions       *session.Service
	Playbooks      playbook.Provider
	AdminKey       string
	Logger         *slog.Logger
	RateLimitRPS   float64
	RateLimitBurst int
	// ClientWS, when set, serves the user-scoped realtime WebSocket at /ws/client.
	ClientWS http.Handler
	// AgentWS, when set, serves the device-scoped agent WebSocket at /ws/agent.
	AgentWS http.Handler
}

// Server is the HTTP + WebSocket server.
type Server struct {
	deps    Deps
	mux     *http.ServeMux
	limiter *rateLimiter
}

// New creates a server with all routes registered.
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{deps: deps, limiter: newRateLimiter(deps.RateLimitRPS, deps.RateLimitBurst)}
	s.routes()
	return s
}

// Handler returns the root handler with middleware applied.
func (s *Server) Handler() http.Handler {
	return s.security(s.instrument(s.mux))
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	obs.Default().WritePrometheus(w)
}

// instrument records request counters and latency.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := obs.Default().StartTimer("conclave_http_request_seconds", "HTTP request duration", obs.Labels{"method": r.Method})
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		timer.Stop()
		obs.Default().Counter("conclave_http_requests_total", "HTTP requests", obs.Labels{
			"method": r.Method, "status": fmt.Sprintf("%d", rec.status),
		}).Inc()
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack lets the WebSocket upgrade work through the instrumentation wrapper.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijack not supported")
	}
	return h.Hijack()
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", s.handleHealth)
	m.HandleFunc("GET /metrics", s.handleMetrics)
	m.HandleFunc("POST /v1/bootstrap", s.admin(s.handleBootstrap))
	m.HandleFunc("POST /v1/users/token", s.admin(s.handleUserToken))
	m.HandleFunc("GET /v1/devices/me", s.auth(s.handleDeviceMe))
	m.HandleFunc("GET /v1/playbooks", s.auth(s.handleListPlaybooks))
	m.HandleFunc("GET /v1/playbooks/{id}", s.auth(s.handleGetPlaybook))
	m.HandleFunc("GET /v1/roles", s.auth(s.handleListRoles))
	s.adminRoutes(m)
	m.HandleFunc("POST /v1/sessions", s.auth(s.handleCreateSession))
	m.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
	m.HandleFunc("GET /v1/sessions/{id}", s.auth(s.handleGetSession))
	m.HandleFunc("POST /v1/sessions/{id}/spec", s.auth(s.handleAddSpec))
	m.HandleFunc("POST /v1/sessions/{id}/runs", s.auth(s.handleRun))
	m.HandleFunc("GET /v1/sessions/{id}/events", s.auth(s.handleEvents))
	m.HandleFunc("GET /v1/sessions/{id}/timeline", s.auth(s.handleTimeline))
	m.HandleFunc("GET /v1/sessions/{id}/messages", s.auth(s.handleListMessages))
	m.HandleFunc("GET /v1/sessions/{id}/jobs", s.auth(s.handleListJobs))
	m.HandleFunc("GET /v1/jobs/{id}", s.auth(s.handleGetJob))
	m.HandleFunc("POST /v1/sessions/{id}/plans", s.auth(s.handleCreatePlan))
	m.HandleFunc("GET /v1/sessions/{id}/plan", s.auth(s.handleGetPlan))
	m.HandleFunc("POST /v1/sessions/{id}/plans/{planID}/resume", s.auth(s.handleResumePlan))
	m.HandleFunc("POST /v1/sessions/{id}/plans/{planID}/replan", s.auth(s.handleReplan))
	m.HandleFunc("GET /v1/sessions/{id}/questions", s.auth(s.handleListQuestions))
	m.HandleFunc("POST /v1/questions/{id}/answer", s.auth(s.handleAnswer))
	m.HandleFunc("POST /v1/sessions/{id}/interview", s.auth(s.handleInterview))
	m.HandleFunc("POST /v1/sessions/{id}/pause", s.auth(s.handlePauseSession))
	m.HandleFunc("POST /v1/sessions/{id}/resume", s.auth(s.handleResumeSession))
	m.HandleFunc("POST /v1/sessions/{id}/interrupt", s.auth(s.handleInterruptSession))
	m.HandleFunc("POST /v1/sessions/{id}/cancel", s.auth(s.handleCancelSession))
	m.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.handleDeleteSession))
	m.HandleFunc("POST /v1/sessions/{id}/message", s.auth(s.handleMessage))
	m.HandleFunc("POST /v1/sessions/{id}/answers", s.auth(s.handleAnswerMany))
	m.HandleFunc("GET /ws", s.handleWS)
	m.HandleFunc("GET /ws/client", s.handleClientWS)
	m.HandleFunc("GET /ws/agent", s.handleAgentWS)
	s.mux = m
}

// --- middleware ---

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const deviceKey ctxKey = 0

func deviceFrom(ctx context.Context) (*domain.Device, bool) {
	d, ok := ctx.Value(deviceKey).(*domain.Device)
	return d, ok
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		dev, err := s.deps.Auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if s.limiter != nil && !s.limiter.allow(dev.TenantID) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), deviceKey, dev)))
	}
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.AdminKey == "" {
			writeError(w, http.StatusServiceUnavailable, "admin key is not configured")
			return
		}
		key := r.Header.Get("X-Admin-Key")
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.deps.AdminKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid admin key")
			return
		}
		next(w, r)
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// --- handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TenantName string `json:"tenant_name"`
		Email      string `json:"email"`
		DeviceName string `json:"device_name"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.TenantName == "" {
		writeError(w, http.StatusBadRequest, "tenant_name is required")
		return
	}
	tenant := domain.Tenant{ID: uuid.NewString(), Name: body.TenantName, CreatedAt: time.Now().UTC()}
	if err := s.deps.Store.CreateTenant(r.Context(), tenant); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user := domain.User{ID: uuid.NewString(), TenantID: tenant.ID, Email: body.Email, CreatedAt: time.Now().UTC()}
	if err := s.deps.Store.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, dev, err := s.deps.Auth.RegisterDevice(r.Context(), tenant.ID, user.ID, body.DeviceName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"tenant_id": tenant.ID, "user_id": user.ID, "device_id": dev.ID, "token": token,
	})
}

// handleUserToken issues a user-scoped token for the client WebSocket.
func (s *Server) handleUserToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	user, err := s.deps.Store.GetUser(r.Context(), body.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	token, ut, err := s.deps.Auth.RegisterUserToken(r.Context(), user.TenantID, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"user_token": token, "user_id": ut.UserID, "tenant_id": ut.TenantID,
	})
}

// handleClientWS serves the user-scoped realtime WebSocket and reads its token
// from the query string (Centrifuge connect token).
func (s *Server) handleClientWS(w http.ResponseWriter, r *http.Request) {
	if s.deps.ClientWS == nil {
		writeError(w, http.StatusServiceUnavailable, "realtime is not configured")
		return
	}
	s.deps.ClientWS.ServeHTTP(w, r)
}

// handleDeviceMe returns the device authenticated by the bearer token.
func (s *Server) handleDeviceMe(w http.ResponseWriter, r *http.Request) {
	dev, _ := deviceFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{
		"device_id": dev.ID, "user_id": dev.UserID, "tenant_id": dev.TenantID,
	})
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	if s.deps.AgentWS == nil {
		writeError(w, http.StatusServiceUnavailable, "agent realtime is not configured")
		return
	}
	s.deps.AgentWS.ServeHTTP(w, r)
}

func (s *Server) handleListPlaybooks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Playbooks.List())
}

func (s *Server) handleGetPlaybook(w http.ResponseWriter, r *http.Request) {
	pb, ok := s.deps.Playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "playbook not found")
		return
	}
	writeJSON(w, http.StatusOK, pb)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	dev, _ := deviceFrom(r.Context())
	var body struct {
		Title      string  `json:"title"`
		ProjectID  string  `json:"project_id"`
		AutoAccept bool    `json:"auto_accept"`
		BudgetUSD  float64 `json:"budget_usd"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sess, err := s.deps.Sessions.CreateSession(r.Context(), session.CreateSessionInput{
		TenantID: dev.TenantID, ProjectID: body.ProjectID, Title: body.Title,
		AutoAccept: body.AutoAccept, BudgetUSD: body.BudgetUSD,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	dev, _ := deviceFrom(r.Context())
	sessions, err := s.deps.Store.ListSessions(r.Context(), dev.TenantID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []domain.Session{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dev, _ := deviceFrom(r.Context())
	sess, err := s.deps.Store.GetSession(r.Context(), id)
	if err != nil || sess.TenantID != dev.TenantID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	arts, _ := s.deps.Store.ListArtifacts(r.Context(), id)
	spec, _ := s.deps.Store.LatestSpec(r.Context(), id)
	questions, _ := s.deps.Store.ListQuestions(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{
		"session":   sess,
		"spec":      spec,
		"artifacts": nonNil(arts),
		"questions": nonNilQ(questions),
	})
}

func (s *Server) handleAddSpec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec, err := s.deps.Sessions.AddSpec(r.Context(), id, body.Content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, spec)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		PlaybookID string `json:"playbook_id"`
		Idea       string `json:"idea"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.PlaybookID == "" {
		writeError(w, http.StatusBadRequest, "playbook_id is required")
		return
	}
	if body.Idea != "" {
		if _, err := s.deps.Sessions.AddSpec(r.Context(), id, body.Idea); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	plan, err := s.deps.Sessions.CreatePlan(r.Context(), session.CreatePlanInput{
		SessionID: id,
		Nodes:     []session.PlanNodeSpec{{ID: "run", PlaybookID: body.PlaybookID}},
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.deps.Sessions.EnqueuePlan(r.Context(), id, plan.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"plan": plan, "job": job})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var from int64
	if v := r.URL.Query().Get("from_seq"); v != "" {
		from, _ = strconv.ParseInt(v, 10, 64)
	}
	evs, err := s.deps.Events.List(r.Context(), id, from)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nonNilE(evs))
}

func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		Auto  bool                   `json:"auto"`
		Nodes []session.PlanNodeSpec `json:"nodes"`
		Edges []session.PlanEdgeSpec `json:"edges"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Auto {
		// Planning uses the LLM, so it runs in a background job.
		job, err := s.deps.Sessions.EnqueuePlanBuild(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
		return
	}
	plan, err := s.deps.Sessions.CreatePlan(r.Context(), session.CreatePlanInput{
		SessionID: id, Nodes: body.Nodes, Edges: body.Edges,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.deps.Sessions.EnqueuePlan(r.Context(), id, plan.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"plan": plan, "job": job})
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	view, err := s.deps.Sessions.PlanReadModel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no active plan")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleResumePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	job, err := s.deps.Sessions.EnqueuePlan(r.Context(), id, r.PathValue("planID"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleReplan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := s.deps.Sessions.Replan(r.Context(), id, r.PathValue("planID"), body.Reason)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.deps.Sessions.EnqueuePlan(r.Context(), id, plan.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"plan": plan, "job": job})
}

func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	qs, err := s.deps.Store.ListQuestions(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nonNilQ(qs))
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	qid := r.PathValue("id")
	q, err := s.deps.Sessions.GetQuestion(r.Context(), qid)
	if err != nil {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}
	dev, _ := deviceFrom(r.Context())
	sess, err := s.deps.Store.GetSession(r.Context(), q.SessionID)
	if err != nil || sess.TenantID != dev.TenantID {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}
	var body struct {
		Selected []string `json:"selected"`
		Custom   string   `json:"custom"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if q.Kind == domain.QuestionKindInterview {
		job, err := s.deps.Sessions.EnqueueInterviewAnswer(r.Context(), q.SessionID, q.Ref, body.Selected, body.Custom)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"answered": true, "job": job})
		return
	}
	if q.Kind == domain.QuestionKindController {
		job, err := s.deps.Sessions.AnswerControllerQuestion(r.Context(), qid, body.Selected, body.Custom)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"answered": true, "job": job})
		return
	}
	job, err := s.deps.Sessions.AnswerQuestion(r.Context(), qid, body.Selected, body.Custom)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"answered": true, "job": job})
}

func (s *Server) handleInterview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		Idea string `json:"idea"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.deps.Sessions.EnqueueInterview(r.Context(), id, body.Idea)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handlePauseSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	if err := s.deps.Sessions.PauseSession(r.Context(), id); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
}

func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	report, err := s.deps.Sessions.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

// handleMessage continues a session with a follow-up instruction. The session
// controller decides what to do; if work is already running the message is
// injected into it.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, injected, err := s.deps.Sessions.Goal(r.Context(), id, body.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if injected {
		writeJSON(w, http.StatusAccepted, map[string]any{"injected": true})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// handleCancelSession stops all work and marks the session cancelled.
func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	n, err := s.deps.Sessions.CancelSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"cancelled": n})
}

// handleDeleteSession removes the session and all of its data.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	if err := s.deps.Sessions.DeleteSession(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// handleAnswerMany submits a questionnaire (a batch of answers) at once.
func (s *Server) handleAnswerMany(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var body struct {
		Answers []session.QuestionAnswer `json:"answers"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.deps.Sessions.AnswerQuestions(r.Context(), id, body.Answers)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"answered": true}
	if job != nil {
		out["job"] = job
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (s *Server) handleInterruptSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	n, err := s.deps.Sessions.InterruptSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"interrupted": n})
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	var from int64
	if v := r.URL.Query().Get("from_seq"); v != "" {
		from, _ = strconv.ParseInt(v, 10, 64)
	}
	view, err := s.deps.Sessions.Timeline(r.Context(), id, from)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	msgs, err := s.deps.Store.ListMessages(r.Context(), id, 2000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if msgs == nil {
		msgs = []domain.Message{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	jobs, err := s.deps.Store.ListJobs(r.Context(), id, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobs == nil {
		jobs = []domain.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.deps.Store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) ownedSession(w http.ResponseWriter, r *http.Request, id string) (*domain.Session, bool) {
	dev, _ := deviceFrom(r.Context())
	sess, err := s.deps.Store.GetSession(r.Context(), id)
	if err != nil || sess.TenantID != dev.TenantID {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, false
	}
	return sess, true
}

// --- helpers ---

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	err := dec.Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func nonNil(v []domain.Artifact) []domain.Artifact {
	if v == nil {
		return []domain.Artifact{}
	}
	return v
}

func nonNilQ(v []domain.Question) []domain.Question {
	if v == nil {
		return []domain.Question{}
	}
	return v
}

func nonNilE(v []domain.Event) []domain.Event {
	if v == nil {
		return []domain.Event{}
	}
	return v
}
