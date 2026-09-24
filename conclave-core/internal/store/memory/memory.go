// Package memory provides an in-memory Store implementation for tests and
// local development without Postgres.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Store is a thread-safe in-memory Store.
type Store struct {
	mu sync.RWMutex

	tenants    map[string]domain.Tenant
	users      map[string]domain.User
	devices    map[string]domain.Device
	userTokens map[string]domain.UserToken
	projects   map[string]domain.Project
	sessions   map[string]domain.Session
	sessAgent  map[string]string
	specs      map[string]domain.Spec
	plans      map[string]domain.Plan
	nodes      map[string]domain.PlanNode
	edges      map[string]domain.PlanEdge
	runs       map[string]domain.PlaybookRun
	tasks      map[string]domain.Task
	attempts   map[string]domain.Attempt
	artifacts  map[string]domain.Artifact
	questions  map[string]domain.Question
	answers    map[string]domain.Answer
	interviews map[string]domain.Interview
	messages   map[string]domain.Message
	jobs       map[string]domain.Job
	events     []domain.Event
	usage      map[string]domain.Usage

	providers      map[string]domain.Provider
	providerModels map[string]domain.ProviderModel
	roles          map[string]domain.Role
	roleGrades     map[string]domain.RoleGrade
	brains         map[string]domain.BrainConfig
	configVersion  int64

	seq int64
}

// New creates an empty in-memory store.
func New() *Store {
	return &Store{
		tenants:    map[string]domain.Tenant{},
		users:      map[string]domain.User{},
		devices:    map[string]domain.Device{},
		userTokens: map[string]domain.UserToken{},
		projects:   map[string]domain.Project{},
		sessions:   map[string]domain.Session{},
		sessAgent:  map[string]string{},
		specs:      map[string]domain.Spec{},
		plans:      map[string]domain.Plan{},
		nodes:      map[string]domain.PlanNode{},
		edges:      map[string]domain.PlanEdge{},
		runs:       map[string]domain.PlaybookRun{},
		tasks:      map[string]domain.Task{},
		attempts:   map[string]domain.Attempt{},
		artifacts:  map[string]domain.Artifact{},
		questions:  map[string]domain.Question{},
		answers:    map[string]domain.Answer{},
		interviews: map[string]domain.Interview{},
		messages:   map[string]domain.Message{},
		jobs:       map[string]domain.Job{},
		usage:      map[string]domain.Usage{},

		providers:      map[string]domain.Provider{},
		providerModels: map[string]domain.ProviderModel{},
		roles:          map[string]domain.Role{},
		roleGrades:     map[string]domain.RoleGrade{},
		brains:         map[string]domain.BrainConfig{},
		configVersion:  1,
	}
}

func (s *Store) Close() error { return nil }

func now() time.Time { return time.Now().UTC() }

// --- Tenancy and identity ---

func (s *Store) CreateTenant(_ context.Context, t domain.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	s.tenants[t.ID] = t
	return nil
}

func (s *Store) GetTenant(_ context.Context, id string) (*domain.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &t, nil
}

func (s *Store) CreateUser(_ context.Context, u domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now()
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) GetUser(_ context.Context, id string) (*domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &u, nil
}

func (s *Store) CreateDevice(_ context.Context, d domain.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now()
	}
	s.devices[d.ID] = d
	return nil
}

func (s *Store) GetDevice(_ context.Context, id string) (*domain.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &d, nil
}

func (s *Store) GetDeviceByTokenHash(_ context.Context, hash string) (*domain.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.devices {
		if d.TokenHash == hash {
			return &d, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) TouchDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	d.LastSeenAt = now()
	s.devices[id] = d
	return nil
}

func (s *Store) RevokeDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	t := now()
	d.RevokedAt = &t
	s.devices[id] = d
	return nil
}

func (s *Store) CreateUserToken(_ context.Context, t domain.UserToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	s.userTokens[t.ID] = t
	return nil
}

func (s *Store) GetUserTokenByHash(_ context.Context, hash string) (*domain.UserToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.userTokens {
		if t.TokenHash == hash {
			cp := t
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) RevokeUserToken(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.userTokens[id]
	if !ok {
		return store.ErrNotFound
	}
	nowT := now()
	t.RevokedAt = &nowT
	s.userTokens[id] = t
	return nil
}

// --- Projects and sessions ---

func (s *Store) CreateProject(_ context.Context, p domain.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	s.projects[p.ID] = p
	return nil
}

func (s *Store) GetProject(_ context.Context, id string) (*domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (s *Store) CreateSession(_ context.Context, sess domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now()
	}
	sess.UpdatedAt = now()
	s.sessions[sess.ID] = sess
	return nil
}

func (s *Store) GetSession(_ context.Context, id string) (*domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &sess, nil
}

func (s *Store) UpdateSessionStatus(_ context.Context, id string, status domain.SessionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return store.ErrNotFound
	}
	if !domain.CanTransitionSession(sess.Status, status) {
		return store.ErrNotFound
	}
	sess.Status = status
	sess.UpdatedAt = now()
	s.sessions[id] = sess
	return nil
}

// DeleteSession removes a session and all of its related records.
func (s *Store) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return store.ErrNotFound
	}
	// plans -> nodes/edges
	planIDs := map[string]bool{}
	for pid, p := range s.plans {
		if p.SessionID == id {
			planIDs[pid] = true
			delete(s.plans, pid)
		}
	}
	for nid, n := range s.nodes {
		if planIDs[n.PlanID] {
			delete(s.nodes, nid)
		}
	}
	for key, e := range s.edges {
		if planIDs[e.PlanID] {
			delete(s.edges, key)
		}
	}
	// runs -> tasks -> attempts
	runIDs := map[string]bool{}
	for rid, r := range s.runs {
		if r.SessionID == id {
			runIDs[rid] = true
			delete(s.runs, rid)
		}
	}
	taskIDs := map[string]bool{}
	for tid, t := range s.tasks {
		if runIDs[t.PlaybookRunID] {
			taskIDs[tid] = true
			delete(s.tasks, tid)
		}
	}
	for aid, a := range s.attempts {
		if taskIDs[a.TaskID] {
			delete(s.attempts, aid)
		}
	}
	// questions -> answers
	questionIDs := map[string]bool{}
	for qid, q := range s.questions {
		if q.SessionID == id {
			questionIDs[qid] = true
			delete(s.questions, qid)
		}
	}
	for aid, a := range s.answers {
		if questionIDs[a.QuestionID] {
			delete(s.answers, aid)
		}
	}
	for mid, m := range s.messages {
		if m.SessionID == id {
			delete(s.messages, mid)
		}
	}
	for jid, j := range s.jobs {
		if j.SessionID == id {
			delete(s.jobs, jid)
		}
	}
	for i, in := range s.interviews {
		if in.SessionID == id {
			delete(s.interviews, i)
		}
	}
	for aid, a := range s.artifacts {
		if a.SessionID == id {
			delete(s.artifacts, aid)
		}
	}
	for sid, sp := range s.specs {
		if sp.SessionID == id {
			delete(s.specs, sid)
		}
	}
	evs := s.events[:0]
	for _, e := range s.events {
		if e.SessionID != id {
			evs = append(evs, e)
		}
	}
	s.events = evs
	delete(s.usage, id)
	delete(s.sessAgent, id)
	delete(s.sessions, id)
	return nil
}

func (s *Store) UpdateSessionTitle(_ context.Context, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return store.ErrNotFound
	}
	sess.Title = title
	sess.UpdatedAt = now()
	s.sessions[id] = sess
	return nil
}

func (s *Store) ListSessions(_ context.Context, tenantID string, limit int) ([]domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Session
	for _, sess := range s.sessions {
		if tenantID == "" || sess.TenantID == tenantID {
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) SetSessionAgent(_ context.Context, sessionID, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessAgent[sessionID] = deviceID
	return nil
}

func (s *Store) GetSessionAgent(_ context.Context, sessionID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dev, ok := s.sessAgent[sessionID]
	if !ok || dev == "" {
		return "", store.ErrNotFound
	}
	return dev, nil
}

// --- Specs and plans ---

func (s *Store) CreateSpec(_ context.Context, sp domain.Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp.CreatedAt.IsZero() {
		sp.CreatedAt = now()
	}
	s.specs[sp.ID] = sp
	return nil
}

func (s *Store) LatestSpec(_ context.Context, sessionID string) (*domain.Spec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *domain.Spec
	for _, sp := range s.specs {
		if sp.SessionID != sessionID {
			continue
		}
		if best == nil || sp.Version > best.Version {
			cp := sp
			best = &cp
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
}

func (s *Store) UpdatePlanStatus(_ context.Context, id string, status domain.PlanStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return store.ErrNotFound
	}
	p.Status = status
	s.plans[id] = p
	return nil
}

func (s *Store) CreatePlan(_ context.Context, p domain.Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	s.plans[p.ID] = p
	return nil
}

func (s *Store) GetPlan(_ context.Context, id string) (*domain.Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (s *Store) ActivePlan(_ context.Context, sessionID string) (*domain.Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *domain.Plan
	for _, p := range s.plans {
		if p.SessionID != sessionID {
			continue
		}
		if best == nil || p.Version > best.Version {
			cp := p
			best = &cp
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
}

func (s *Store) CreatePlanNode(_ context.Context, n domain.PlanNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.Kind == "" {
		n.Kind = domain.NodeKindPlaybook
	}
	if n.Key == "" {
		n.Key = n.ID
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now()
	}
	n.UpdatedAt = now()
	s.nodes[n.ID] = n
	return nil
}

func (s *Store) GetPlanNode(_ context.Context, id string) (*domain.PlanNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &n, nil
}

func (s *Store) GetPlanNodeByKey(_ context.Context, planID, key string) (*domain.PlanNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.nodes {
		if n.PlanID == planID && n.Key == key {
			cp := n
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) UpdatePlanNodeState(_ context.Context, id string, state domain.NodeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return store.ErrNotFound
	}
	if err := domain.TransitionNode(n.State, state); err != nil {
		return err
	}
	n.State = state
	n.UpdatedAt = now()
	s.nodes[id] = n
	return nil
}

func (s *Store) UpdatePlanNodeOutput(_ context.Context, id, output string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return store.ErrNotFound
	}
	n.Output = output
	n.UpdatedAt = now()
	s.nodes[id] = n
	return nil
}

func (s *Store) ListPlanNodes(_ context.Context, planID string) ([]domain.PlanNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.PlanNode
	for _, n := range s.nodes {
		if n.PlanID == planID {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) CreatePlanEdge(_ context.Context, e domain.PlanEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edges[e.PlanID+"|"+e.From+"|"+e.To] = e
	return nil
}

func (s *Store) ListPlanEdges(_ context.Context, planID string) ([]domain.PlanEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.PlanEdge
	for _, e := range s.edges {
		if e.PlanID == planID {
			out = append(out, e)
		}
	}
	return out, nil
}

// --- Playbook runs and tasks ---

func (s *Store) CreatePlaybookRun(_ context.Context, r domain.PlaybookRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now()
	}
	r.UpdatedAt = now()
	s.runs[r.ID] = r
	return nil
}

func (s *Store) GetPlaybookRun(_ context.Context, id string) (*domain.PlaybookRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (s *Store) UpdatePlaybookRunState(_ context.Context, id string, state domain.NodeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return store.ErrNotFound
	}
	if err := domain.TransitionNode(r.State, state); err != nil {
		return err
	}
	r.State = state
	r.UpdatedAt = now()
	s.runs[id] = r
	return nil
}

func (s *Store) CreateTask(_ context.Context, t domain.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	t.UpdatedAt = now()
	s.tasks[t.ID] = t
	return nil
}

func (s *Store) UpdateTaskState(_ context.Context, id string, state domain.TaskState, attempts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return store.ErrNotFound
	}
	if !domain.CanTransitionTask(t.State, state) {
		return store.ErrNotFound
	}
	t.State = state
	t.Attempts = attempts
	t.UpdatedAt = now()
	s.tasks[id] = t
	return nil
}

func (s *Store) ListTasks(_ context.Context, playbookRunID string) ([]domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Task
	for _, t := range s.tasks {
		if t.PlaybookRunID == playbookRunID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) CreateAttempt(_ context.Context, a domain.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now()
	}
	s.attempts[a.ID] = a
	return nil
}

// --- Artifacts, questions, answers ---

func (s *Store) CreateArtifact(_ context.Context, a domain.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now()
	}
	s.artifacts[a.ID] = a
	return nil
}

func (s *Store) ListArtifacts(_ context.Context, sessionID string) ([]domain.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Artifact
	for _, a := range s.artifacts {
		if a.SessionID == sessionID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) CreateQuestion(_ context.Context, q domain.Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.CreatedAt.IsZero() {
		q.CreatedAt = now()
	}
	s.questions[q.ID] = q
	return nil
}

func (s *Store) GetQuestion(_ context.Context, id string) (*domain.Question, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.questions[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &q, nil
}

func (s *Store) UpdateQuestionState(_ context.Context, id string, state domain.QuestionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.questions[id]
	if !ok {
		return store.ErrNotFound
	}
	q.State = state
	s.questions[id] = q
	return nil
}

func (s *Store) ListQuestions(_ context.Context, sessionID string) ([]domain.Question, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Question
	for _, q := range s.questions {
		if q.SessionID == sessionID {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) CreateAnswer(_ context.Context, a domain.Answer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now()
	}
	s.answers[a.QuestionID] = a
	return nil
}

func (s *Store) GetAnswer(_ context.Context, questionID string) (*domain.Answer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.answers[questionID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &a, nil
}

// --- Interviews ---

func (s *Store) CreateInterview(_ context.Context, in domain.Interview) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now()
	}
	in.UpdatedAt = now()
	s.interviews[in.ID] = in
	return nil
}

func (s *Store) GetInterview(_ context.Context, id string) (*domain.Interview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.interviews[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &in, nil
}

func (s *Store) UpdateInterview(_ context.Context, in domain.Interview) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.interviews[in.ID]; !ok {
		return store.ErrNotFound
	}
	in.UpdatedAt = now()
	s.interviews[in.ID] = in
	return nil
}

// --- Messages ---

func (s *Store) UpsertMessage(_ context.Context, m domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.messages[m.ID]; ok {
		m.CreatedAt = existing.CreatedAt
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now()
	}
	m.UpdatedAt = now()
	s.messages[m.ID] = m
	return nil
}

func (s *Store) FinalizeMessage(_ context.Context, id, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[id]
	if !ok {
		return store.ErrNotFound
	}
	if content != "" {
		m.Content = content
	}
	m.State = "done"
	m.UpdatedAt = now()
	s.messages[id] = m
	return nil
}

func (s *Store) ListMessages(_ context.Context, sessionID string, limit int) ([]domain.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Message
	for _, m := range s.messages {
		if m.SessionID == sessionID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PendingMessages returns user messages not yet consumed by the brain/roles.
func (s *Store) PendingMessages(_ context.Context, sessionID string) ([]domain.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Message
	for _, m := range s.messages {
		if m.SessionID == sessionID && m.Kind == domain.MessageKindUser && m.State == domain.MessageStatePending {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// MarkMessagesInjected marks user messages as consumed.
func (s *Store) MarkMessagesInjected(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		m, ok := s.messages[id]
		if !ok {
			continue
		}
		m.State = domain.MessageStateInjected
		m.UpdatedAt = now()
		s.messages[id] = m
	}
	return nil
}

// --- Jobs ---

func (s *Store) EnqueueJob(_ context.Context, j domain.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.State == "" {
		j.State = domain.JobQueued
	}
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = 3
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now()
	}
	j.UpdatedAt = now()
	s.jobs[j.ID] = j
	return nil
}

func (s *Store) GetJob(_ context.Context, id string) (*domain.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &j, nil
}

func (s *Store) ClaimJob(_ context.Context, workerID string, lease time.Duration) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := now()
	// Sweep: a job with a cancel request is not claimable; mark it cancelled.
	for id, j := range s.jobs {
		if !j.CancelRequested {
			continue
		}
		cancellable := j.State == domain.JobQueued ||
			(j.State == domain.JobLeased && !j.LeaseExpiresAt.IsZero() && j.LeaseExpiresAt.Before(t))
		if cancellable {
			j.State = domain.JobCancelled
			j.Error = "cancelled"
			j.LeasedBy = ""
			j.LeaseExpiresAt = time.Time{}
			j.UpdatedAt = t
			s.jobs[id] = j
		}
	}
	var best *domain.Job
	for _, j := range s.jobs {
		claimable := j.State == domain.JobQueued || (j.State == domain.JobLeased && !j.LeaseExpiresAt.IsZero() && j.LeaseExpiresAt.Before(t))
		if !claimable {
			continue
		}
		if best == nil || j.CreatedAt.Before(best.CreatedAt) {
			cp := j
			best = &cp
		}
	}
	if best == nil {
		return nil, nil
	}
	best.State = domain.JobLeased
	best.LeasedBy = workerID
	best.Attempts++
	best.LeaseExpiresAt = t.Add(lease)
	best.HeartbeatAt = t
	best.UpdatedAt = t
	s.jobs[best.ID] = *best
	return best, nil
}

func (s *Store) HeartbeatJob(_ context.Context, id, workerID string, lease time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	if j.LeasedBy != workerID {
		return store.ErrNotFound
	}
	t := now()
	j.HeartbeatAt = t
	j.LeaseExpiresAt = t.Add(lease)
	j.UpdatedAt = t
	s.jobs[id] = j
	return nil
}

func (s *Store) CompleteJob(_ context.Context, id string, state domain.JobState, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	j.State = state
	j.Error = errMsg
	j.LeasedBy = ""
	j.LeaseExpiresAt = time.Time{}
	j.UpdatedAt = now()
	s.jobs[id] = j
	return nil
}

func (s *Store) RequestJobCancel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return store.ErrNotFound
	}
	j.CancelRequested = true
	j.UpdatedAt = now()
	s.jobs[id] = j
	return nil
}

func (s *Store) JobCancelRequested(_ context.Context, id string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return false, store.ErrNotFound
	}
	return j.CancelRequested, nil
}

func (s *Store) ListJobs(_ context.Context, sessionID string, limit int) ([]domain.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Job
	for _, j := range s.jobs {
		if sessionID == "" || j.SessionID == sessionID {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- Event log and usage ---

func (s *Store) AppendEvent(_ context.Context, e domain.Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.Seq = s.seq
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now()
	}
	s.events = append(s.events, e)
	return e.Seq, nil
}

func (s *Store) ListEvents(_ context.Context, sessionID string, fromSeq int64) ([]domain.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Event
	for _, e := range s.events {
		if e.SessionID != sessionID || e.Seq <= fromSeq {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) RecordUsage(_ context.Context, u domain.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.usage[u.SessionID]
	cur.SessionID = u.SessionID
	cur.PromptTokens += u.PromptTokens
	cur.CompletionTokens += u.CompletionTokens
	cur.CostUSD += u.CostUSD
	s.usage[u.SessionID] = cur
	return nil
}

func (s *Store) TotalUsage(_ context.Context, sessionID string) (domain.Usage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usage[sessionID], nil
}
