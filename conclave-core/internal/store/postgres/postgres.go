// Package postgres implements store.Store on top of PostgreSQL via pgx.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is a PostgreSQL-backed store.Store.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and applies pending migrations.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	applied := map[int]bool{}
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version, err := parseVersion(name)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes), pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func parseVersion(name string) (int, error) {
	base := strings.SplitN(name, "_", 2)[0]
	return strconv.Atoi(base)
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

// --- Tenancy and identity ---

func (s *Store) CreateTenant(ctx context.Context, t domain.Tenant) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tenants (id, name, created_at) VALUES ($1,$2,$3)`,
		t.ID, t.Name, ensureTime(t.CreatedAt))
	return err
}

func (s *Store) GetTenant(ctx context.Context, id string) (*domain.Tenant, error) {
	var t domain.Tenant
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, created_at FROM tenants WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &t, nil
}

func (s *Store) CreateUser(ctx context.Context, u domain.User) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, tenant_id, email, created_at) VALUES ($1,$2,$3,$4)`,
		u.ID, u.TenantID, u.Email, ensureTime(u.CreatedAt))
	return err
}

func (s *Store) GetUser(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, email, created_at FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &u, nil
}

func (s *Store) CreateDevice(ctx context.Context, d domain.Device) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO devices (id, tenant_id, user_id, name, token_hash, created_at, last_seen_at, revoked_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		d.ID, d.TenantID, d.UserID, d.Name, d.TokenHash, ensureTime(d.CreatedAt), nullTime(d.LastSeenAt), d.RevokedAt)
	return err
}

func (s *Store) GetDevice(ctx context.Context, id string) (*domain.Device, error) {
	return s.scanDevice(s.pool.QueryRow(ctx, deviceSelect+` WHERE id=$1`, id))
}

func (s *Store) GetDeviceByTokenHash(ctx context.Context, hash string) (*domain.Device, error) {
	return s.scanDevice(s.pool.QueryRow(ctx, deviceSelect+` WHERE token_hash=$1`, hash))
}

const deviceSelect = `SELECT id, tenant_id, user_id, name, token_hash, created_at, last_seen_at, revoked_at FROM devices`

func (s *Store) scanDevice(row pgx.Row) (*domain.Device, error) {
	var d domain.Device
	var lastSeen, revoked *time.Time
	if err := row.Scan(&d.ID, &d.TenantID, &d.UserID, &d.Name, &d.TokenHash, &d.CreatedAt, &lastSeen, &revoked); err != nil {
		return nil, mapNotFound(err)
	}
	if lastSeen != nil {
		d.LastSeenAt = *lastSeen
	}
	d.RevokedAt = revoked
	return &d, nil
}

func (s *Store) TouchDevice(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET last_seen_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, id)
	return err
}

// --- User tokens ---

func (s *Store) CreateUserToken(ctx context.Context, t domain.UserToken) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_tokens (id, tenant_id, user_id, token_hash, created_at, revoked_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		t.ID, t.TenantID, t.UserID, t.TokenHash, ensureTime(t.CreatedAt), t.RevokedAt)
	return err
}

func (s *Store) GetUserTokenByHash(ctx context.Context, hash string) (*domain.UserToken, error) {
	var t domain.UserToken
	var revoked *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, user_id, token_hash, created_at, revoked_at FROM user_tokens WHERE token_hash=$1`, hash).
		Scan(&t.ID, &t.TenantID, &t.UserID, &t.TokenHash, &t.CreatedAt, &revoked)
	if err != nil {
		return nil, mapNotFound(err)
	}
	t.RevokedAt = revoked
	return &t, nil
}

func (s *Store) RevokeUserToken(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_tokens SET revoked_at=now() WHERE id=$1`, id)
	return err
}

// --- Projects and sessions ---

func (s *Store) CreateProject(ctx context.Context, p domain.Project) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO projects (id, tenant_id, name, repo_path, created_at) VALUES ($1,$2,$3,$4,$5)`,
		p.ID, p.TenantID, p.Name, p.RepoPath, ensureTime(p.CreatedAt))
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (*domain.Project, error) {
	var p domain.Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, name, repo_path, created_at FROM projects WHERE id=$1`, id).
		Scan(&p.ID, &p.TenantID, &p.Name, &p.RepoPath, &p.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

func (s *Store) CreateSession(ctx context.Context, sess domain.Session) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, tenant_id, project_id, title, status, auto_accept, budget_usd, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		sess.ID, sess.TenantID, sess.ProjectID, sess.Title, sess.Status, sess.AutoAccept, sess.BudgetUSD, ensureTime(sess.CreatedAt), ensureTime(sess.UpdatedAt))
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	var sess domain.Session
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, project_id, title, status, auto_accept, budget_usd, created_at, updated_at FROM sessions WHERE id=$1`, id).
		Scan(&sess.ID, &sess.TenantID, &sess.ProjectID, &sess.Title, &sess.Status, &sess.AutoAccept, &sess.BudgetUSD, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &sess, nil
}

func (s *Store) UpdateSessionStatus(ctx context.Context, id string, status domain.SessionStatus) error {
	cur, err := s.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !domain.CanTransitionSession(cur.Status, status) {
		return fmt.Errorf("invalid session transition %s -> %s", cur.Status, status)
	}
	_, err = s.pool.Exec(ctx, `UPDATE sessions SET status=$1, updated_at=now() WHERE id=$2`, status, id)
	return err
}

func (s *Store) UpdateSessionTitle(ctx context.Context, id, title string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET title=$1, updated_at=now() WHERE id=$2`, title, id)
	return err
}

// DeleteSession removes a session and all of its related records.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stmts := []string{
		`DELETE FROM attempts WHERE task_id IN (
		   SELECT id FROM tasks WHERE playbook_run_id IN (
		     SELECT id FROM playbook_runs WHERE session_id=$1))`,
		`DELETE FROM tasks WHERE playbook_run_id IN (SELECT id FROM playbook_runs WHERE session_id=$1)`,
		`DELETE FROM answers WHERE question_id IN (SELECT id FROM questions WHERE session_id=$1)`,
		`DELETE FROM questions WHERE session_id=$1`,
		`DELETE FROM plan_edges WHERE plan_id IN (SELECT id FROM plans WHERE session_id=$1)`,
		`DELETE FROM plan_nodes WHERE plan_id IN (SELECT id FROM plans WHERE session_id=$1)`,
		`DELETE FROM plans WHERE session_id=$1`,
		`DELETE FROM playbook_runs WHERE session_id=$1`,
		`DELETE FROM artifacts WHERE session_id=$1`,
		`DELETE FROM specs WHERE session_id=$1`,
		`DELETE FROM interviews WHERE session_id=$1`,
		`DELETE FROM messages WHERE session_id=$1`,
		`DELETE FROM jobs WHERE session_id=$1`,
		`DELETE FROM events WHERE session_id=$1`,
		`DELETE FROM usage_totals WHERE session_id=$1`,
		`DELETE FROM session_agents WHERE session_id=$1`,
		`DELETE FROM sessions WHERE id=$1`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListSessions(ctx context.Context, tenantID string, limit int) ([]domain.Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, project_id, title, status, auto_accept, budget_usd, created_at, updated_at
		 FROM sessions WHERE ($1='' OR tenant_id=$1) ORDER BY created_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Session
	for rows.Next() {
		var sess domain.Session
		if err := rows.Scan(&sess.ID, &sess.TenantID, &sess.ProjectID, &sess.Title, &sess.Status, &sess.AutoAccept, &sess.BudgetUSD, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) SetSessionAgent(ctx context.Context, sessionID, deviceID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO session_agents (session_id, device_id, updated_at) VALUES ($1,$2,now())
		 ON CONFLICT (session_id) DO UPDATE SET device_id=excluded.device_id, updated_at=now()`,
		sessionID, deviceID)
	return err
}

func (s *Store) GetSessionAgent(ctx context.Context, sessionID string) (string, error) {
	var deviceID string
	err := s.pool.QueryRow(ctx, `SELECT device_id FROM session_agents WHERE session_id=$1`, sessionID).Scan(&deviceID)
	if err != nil {
		return "", mapNotFound(err)
	}
	return deviceID, nil
}

// --- Specs and plans ---

func (s *Store) CreateSpec(ctx context.Context, sp domain.Spec) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO specs (id, session_id, version, content, created_at) VALUES ($1,$2,$3,$4,$5)`,
		sp.ID, sp.SessionID, sp.Version, sp.Content, ensureTime(sp.CreatedAt))
	return err
}

func (s *Store) LatestSpec(ctx context.Context, sessionID string) (*domain.Spec, error) {
	var sp domain.Spec
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, version, content, created_at FROM specs WHERE session_id=$1 ORDER BY version DESC LIMIT 1`, sessionID).
		Scan(&sp.ID, &sp.SessionID, &sp.Version, &sp.Content, &sp.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &sp, nil
}

func (s *Store) CreatePlan(ctx context.Context, p domain.Plan) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plans (id, session_id, version, status, created_at) VALUES ($1,$2,$3,$4,$5)`,
		p.ID, p.SessionID, p.Version, p.Status, ensureTime(p.CreatedAt))
	return err
}

func (s *Store) GetPlan(ctx context.Context, id string) (*domain.Plan, error) {
	var p domain.Plan
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, version, status, created_at FROM plans WHERE id=$1`, id).
		Scan(&p.ID, &p.SessionID, &p.Version, &p.Status, &p.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

func (s *Store) ActivePlan(ctx context.Context, sessionID string) (*domain.Plan, error) {
	var p domain.Plan
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, version, status, created_at FROM plans WHERE session_id=$1 ORDER BY version DESC LIMIT 1`, sessionID).
		Scan(&p.ID, &p.SessionID, &p.Version, &p.Status, &p.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &p, nil
}

func (s *Store) UpdatePlanStatus(ctx context.Context, id string, status domain.PlanStatus) error {
	_, err := s.pool.Exec(ctx, `UPDATE plans SET status=$1 WHERE id=$2`, status, id)
	return err
}

func (s *Store) CreatePlanNode(ctx context.Context, n domain.PlanNode) error {
	if n.Kind == "" {
		n.Kind = domain.NodeKindPlaybook
	}
	if n.Key == "" {
		n.Key = n.ID
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plan_nodes (id, node_key, plan_id, kind, playbook_id, role_id, grade, playbook_version, title, prompt, output, state, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		n.ID, n.Key, n.PlanID, n.Kind, n.PlaybookID, n.RoleID, n.Grade, n.PlaybookVersion, n.Title, n.Prompt, n.Output, n.State, ensureTime(n.CreatedAt), ensureTime(n.UpdatedAt))
	return err
}

func (s *Store) UpdatePlanNodeOutput(ctx context.Context, id, output string) error {
	_, err := s.pool.Exec(ctx, `UPDATE plan_nodes SET output=$1, updated_at=now() WHERE id=$2`, output, id)
	return err
}

const planNodeSelect = `SELECT id, COALESCE(node_key,''), plan_id, kind, COALESCE(playbook_id,''), COALESCE(role_id,''), COALESCE(grade,''), playbook_version, title, prompt, output, state, created_at, updated_at FROM plan_nodes`

func scanPlanNode(row pgx.Row) (*domain.PlanNode, error) {
	var n domain.PlanNode
	err := row.Scan(&n.ID, &n.Key, &n.PlanID, &n.Kind, &n.PlaybookID, &n.RoleID, &n.Grade, &n.PlaybookVersion, &n.Title, &n.Prompt, &n.Output, &n.State, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &n, nil
}

func (s *Store) GetPlanNode(ctx context.Context, id string) (*domain.PlanNode, error) {
	return scanPlanNode(s.pool.QueryRow(ctx, planNodeSelect+` WHERE id=$1`, id))
}

func (s *Store) GetPlanNodeByKey(ctx context.Context, planID, key string) (*domain.PlanNode, error) {
	return scanPlanNode(s.pool.QueryRow(ctx, planNodeSelect+` WHERE plan_id=$1 AND node_key=$2`, planID, key))
}

func (s *Store) UpdatePlanNodeState(ctx context.Context, id string, state domain.NodeState) error {
	var cur domain.NodeState
	if err := s.pool.QueryRow(ctx, `SELECT state FROM plan_nodes WHERE id=$1`, id).Scan(&cur); err != nil {
		return mapNotFound(err)
	}
	if err := domain.TransitionNode(cur, state); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE plan_nodes SET state=$1, updated_at=now() WHERE id=$2`, state, id)
	return err
}

func (s *Store) ListPlanNodes(ctx context.Context, planID string) ([]domain.PlanNode, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, COALESCE(node_key,''), plan_id, kind, COALESCE(playbook_id,''), COALESCE(role_id,''), COALESCE(grade,''), playbook_version, title, prompt, output, state, created_at, updated_at
		 FROM plan_nodes WHERE plan_id=$1 ORDER BY created_at`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PlanNode
	for rows.Next() {
		var n domain.PlanNode
		if err := rows.Scan(&n.ID, &n.Key, &n.PlanID, &n.Kind, &n.PlaybookID, &n.RoleID, &n.Grade, &n.PlaybookVersion, &n.Title, &n.Prompt, &n.Output, &n.State, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CreatePlanEdge(ctx context.Context, e domain.PlanEdge) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO plan_edges (plan_id, from_node, to_node, kind) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (plan_id, from_node, to_node) DO UPDATE SET kind=excluded.kind`,
		e.PlanID, e.From, e.To, e.Kind)
	return err
}

func (s *Store) ListPlanEdges(ctx context.Context, planID string) ([]domain.PlanEdge, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT plan_id, from_node, to_node, kind FROM plan_edges WHERE plan_id=$1`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PlanEdge
	for rows.Next() {
		var e domain.PlanEdge
		if err := rows.Scan(&e.PlanID, &e.From, &e.To, &e.Kind); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Playbook runs and tasks ---

func (s *Store) CreatePlaybookRun(ctx context.Context, r domain.PlaybookRun) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO playbook_runs (id, session_id, plan_node_id, playbook_id, playbook_version, state, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		r.ID, r.SessionID, r.PlanNodeID, r.PlaybookID, r.PlaybookVersion, r.State, ensureTime(r.CreatedAt), ensureTime(r.UpdatedAt))
	return err
}

func (s *Store) GetPlaybookRun(ctx context.Context, id string) (*domain.PlaybookRun, error) {
	var r domain.PlaybookRun
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, plan_node_id, playbook_id, playbook_version, state, created_at, updated_at
		 FROM playbook_runs WHERE id=$1`, id).
		Scan(&r.ID, &r.SessionID, &r.PlanNodeID, &r.PlaybookID, &r.PlaybookVersion, &r.State, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &r, nil
}

func (s *Store) UpdatePlaybookRunState(ctx context.Context, id string, state domain.NodeState) error {
	var cur domain.NodeState
	if err := s.pool.QueryRow(ctx, `SELECT state FROM playbook_runs WHERE id=$1`, id).Scan(&cur); err != nil {
		return mapNotFound(err)
	}
	if err := domain.TransitionNode(cur, state); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE playbook_runs SET state=$1, updated_at=now() WHERE id=$2`, state, id)
	return err
}

func (s *Store) CreateTask(ctx context.Context, t domain.Task) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tasks (id, playbook_run_id, title, tier, state, attempts, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.PlaybookRunID, t.Title, t.Tier, t.State, t.Attempts, ensureTime(t.CreatedAt), ensureTime(t.UpdatedAt))
	return err
}

func (s *Store) UpdateTaskState(ctx context.Context, id string, state domain.TaskState, attempts int) error {
	var cur domain.TaskState
	if err := s.pool.QueryRow(ctx, `SELECT state FROM tasks WHERE id=$1`, id).Scan(&cur); err != nil {
		return mapNotFound(err)
	}
	if !domain.CanTransitionTask(cur, state) {
		return fmt.Errorf("invalid task transition %s -> %s", cur, state)
	}
	_, err := s.pool.Exec(ctx, `UPDATE tasks SET state=$1, attempts=$2, updated_at=now() WHERE id=$3`, state, attempts, id)
	return err
}

func (s *Store) ListTasks(ctx context.Context, playbookRunID string) ([]domain.Task, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, playbook_run_id, title, tier, state, attempts, created_at, updated_at
		 FROM tasks WHERE playbook_run_id=$1 ORDER BY created_at`, playbookRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.PlaybookRunID, &t.Title, &t.Tier, &t.State, &t.Attempts, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateAttempt(ctx context.Context, a domain.Attempt) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO attempts (id, task_id, n, state, error, created_at, finished_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		a.ID, a.TaskID, a.N, a.State, a.Error, ensureTime(a.CreatedAt), nullTime(a.FinishedAt))
	return err
}

// --- Artifacts, questions, answers ---

func (s *Store) CreateArtifact(ctx context.Context, a domain.Artifact) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO artifacts (id, session_id, playbook_run_id, name, kind, ref, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		a.ID, a.SessionID, a.PlaybookRunID, a.Name, a.Kind, a.Ref, ensureTime(a.CreatedAt))
	return err
}

func (s *Store) ListArtifacts(ctx context.Context, sessionID string) ([]domain.Artifact, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, playbook_run_id, name, kind, ref, created_at
		 FROM artifacts WHERE session_id=$1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Artifact
	for rows.Next() {
		var a domain.Artifact
		if err := rows.Scan(&a.ID, &a.SessionID, &a.PlaybookRunID, &a.Name, &a.Kind, &a.Ref, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateQuestion(ctx context.Context, q domain.Question) error {
	if q.Kind == "" {
		q.Kind = domain.QuestionKindGate
	}
	opts, _ := json.Marshal(q.Options)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO questions (id, session_id, kind, ref, text, options, auto_policy, state, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		q.ID, q.SessionID, q.Kind, q.Ref, q.Text, opts, q.AutoPolicy, q.State, ensureTime(q.CreatedAt))
	return err
}

func (s *Store) GetQuestion(ctx context.Context, id string) (*domain.Question, error) {
	var q domain.Question
	var opts []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, kind, ref, text, options, auto_policy, state, created_at
		 FROM questions WHERE id=$1`, id).
		Scan(&q.ID, &q.SessionID, &q.Kind, &q.Ref, &q.Text, &opts, &q.AutoPolicy, &q.State, &q.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	_ = json.Unmarshal(opts, &q.Options)
	return &q, nil
}

func (s *Store) UpdateQuestionState(ctx context.Context, id string, state domain.QuestionState) error {
	_, err := s.pool.Exec(ctx, `UPDATE questions SET state=$1 WHERE id=$2`, state, id)
	return err
}

func (s *Store) ListQuestions(ctx context.Context, sessionID string) ([]domain.Question, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, kind, ref, text, options, auto_policy, state, created_at
		 FROM questions WHERE session_id=$1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Question
	for rows.Next() {
		var q domain.Question
		var opts []byte
		if err := rows.Scan(&q.ID, &q.SessionID, &q.Kind, &q.Ref, &q.Text, &opts, &q.AutoPolicy, &q.State, &q.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(opts, &q.Options)
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) CreateAnswer(ctx context.Context, a domain.Answer) error {
	sel, _ := json.Marshal(a.Selected)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO answers (id, question_id, selected, custom, auto, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		a.ID, a.QuestionID, sel, a.Custom, a.Auto, ensureTime(a.CreatedAt))
	return err
}

func (s *Store) GetAnswer(ctx context.Context, questionID string) (*domain.Answer, error) {
	var a domain.Answer
	var sel []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, question_id, selected, custom, auto, created_at FROM answers WHERE question_id=$1`, questionID).
		Scan(&a.ID, &a.QuestionID, &sel, &a.Custom, &a.Auto, &a.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	_ = json.Unmarshal(sel, &a.Selected)
	return &a, nil
}

// --- Interviews ---

func (s *Store) CreateInterview(ctx context.Context, in domain.Interview) error {
	transcript, _ := json.Marshal(in.Transcript)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO interviews (id, session_id, idea, state, pending_question_id, transcript, spec_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		in.ID, in.SessionID, in.Idea, in.State, in.PendingQuestionID, transcript, in.SpecID,
		ensureTime(in.CreatedAt), ensureTime(in.UpdatedAt))
	return err
}

func (s *Store) GetInterview(ctx context.Context, id string) (*domain.Interview, error) {
	var in domain.Interview
	var transcript []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, session_id, idea, state, pending_question_id, transcript, spec_id, created_at, updated_at
		 FROM interviews WHERE id=$1`, id).
		Scan(&in.ID, &in.SessionID, &in.Idea, &in.State, &in.PendingQuestionID, &transcript, &in.SpecID, &in.CreatedAt, &in.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	_ = json.Unmarshal(transcript, &in.Transcript)
	return &in, nil
}

func (s *Store) UpdateInterview(ctx context.Context, in domain.Interview) error {
	transcript, _ := json.Marshal(in.Transcript)
	_, err := s.pool.Exec(ctx,
		`UPDATE interviews SET state=$1, pending_question_id=$2, transcript=$3, spec_id=$4, updated_at=now() WHERE id=$5`,
		in.State, in.PendingQuestionID, transcript, in.SpecID, in.ID)
	return err
}

// --- Messages ---

func (s *Store) UpsertMessage(ctx context.Context, m domain.Message) error {
	if m.Kind == "" {
		m.Kind = "assistant"
	}
	if m.State == "" {
		m.State = "streaming"
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO messages (id, session_id, playbook_run_id, node_id, role, model, kind, content, state, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
		 ON CONFLICT (id) DO UPDATE SET
		   content=excluded.content, state=excluded.state, role=excluded.role, model=excluded.model,
		   node_id=excluded.node_id, updated_at=now()`,
		m.ID, m.SessionID, m.PlaybookRunID, m.NodeID, m.Role, m.Model, m.Kind, m.Content, m.State, ensureTime(m.CreatedAt))
	return err
}

func (s *Store) FinalizeMessage(ctx context.Context, id, content string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE messages SET state='done', content=CASE WHEN $2 <> '' THEN $2 ELSE content END, updated_at=now() WHERE id=$1`,
		id, content)
	return err
}

func (s *Store) ListMessages(ctx context.Context, sessionID string, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, COALESCE(playbook_run_id,''), COALESCE(node_id,''), COALESCE(role,''), COALESCE(model,''),
		        COALESCE(kind,'assistant'), COALESCE(content,''), COALESCE(state,'done'), created_at, updated_at
		 FROM messages WHERE session_id=$1 ORDER BY created_at LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.PlaybookRunID, &m.NodeID, &m.Role, &m.Model, &m.Kind, &m.Content, &m.State, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingMessages returns user messages not yet consumed by the brain/roles.
func (s *Store) PendingMessages(ctx context.Context, sessionID string) ([]domain.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, COALESCE(playbook_run_id,''), COALESCE(node_id,''), COALESCE(role,''), COALESCE(model,''),
		        COALESCE(kind,'user'), COALESCE(content,''), COALESCE(state,'pending'), created_at, updated_at
		 FROM messages WHERE session_id=$1 AND kind='user' AND state='pending' ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.SessionID, &m.PlaybookRunID, &m.NodeID, &m.Role, &m.Model, &m.Kind, &m.Content, &m.State, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkMessagesInjected marks user messages as consumed.
func (s *Store) MarkMessagesInjected(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE messages SET state='injected', updated_at=now() WHERE id = ANY($1)`, ids)
	return err
}

// --- Jobs ---

const jobSelect = `SELECT id, kind, session_id, plan_id, state, attempts, max_attempts,
	COALESCE(leased_by,''), lease_expires_at, heartbeat_at, COALESCE(error,''), cancel_requested, COALESCE(payload_json,''), created_at, updated_at FROM jobs`

func (s *Store) EnqueueJob(ctx context.Context, j domain.Job) error {
	if j.State == "" {
		j.State = domain.JobQueued
	}
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = 3
	}
	payload := string(j.Payload)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (id, kind, session_id, plan_id, state, attempts, max_attempts, payload_json, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		j.ID, j.Kind, j.SessionID, j.PlanID, j.State, j.Attempts, j.MaxAttempts, payload, ensureTime(j.CreatedAt), ensureTime(j.UpdatedAt))
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, jobSelect+` WHERE id=$1`, id))
}

func (s *Store) ClaimJob(ctx context.Context, workerID string, lease time.Duration) (*domain.Job, error) {
	// Sweep: cancelled queued jobs (and expired leased ones) are not claimable.
	if _, err := s.pool.Exec(ctx,
		`UPDATE jobs SET state='cancelled', error='cancelled', leased_by='', lease_expires_at=NULL, updated_at=now()
		 WHERE cancel_requested = true AND (
		   state='queued' OR (state='leased' AND (lease_expires_at IS NULL OR lease_expires_at < now())))`); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE jobs SET state='leased', leased_by=$1, lease_expires_at=now()+make_interval(secs => $2),
		        heartbeat_at=now(), attempts=attempts+1, updated_at=now()
		 WHERE id = (
		   SELECT id FROM jobs
		   WHERE cancel_requested = false AND (
		     state='queued' OR (state='leased' AND lease_expires_at IS NOT NULL AND lease_expires_at < now()))
		   ORDER BY created_at
		   FOR UPDATE SKIP LOCKED
		   LIMIT 1
		 )
		 RETURNING id, kind, session_id, plan_id, state, attempts, max_attempts,
		           COALESCE(leased_by,''), lease_expires_at, heartbeat_at, COALESCE(error,''), cancel_requested, COALESCE(payload_json,''), created_at, updated_at`,
		workerID, lease.Seconds())
	job, err := scanJob(row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return job, err
}

func (s *Store) HeartbeatJob(ctx context.Context, id, workerID string, lease time.Duration) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET heartbeat_at=now(), lease_expires_at=now()+make_interval(secs => $1), updated_at=now()
		 WHERE id=$2 AND leased_by=$3`, lease.Seconds(), id, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CompleteJob(ctx context.Context, id string, state domain.JobState, errMsg string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET state=$1, error=$2, leased_by='', lease_expires_at=NULL, updated_at=now() WHERE id=$3`,
		state, errMsg, id)
	return err
}

func (s *Store) RequestJobCancel(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET cancel_requested=true, updated_at=now() WHERE id=$1`, id)
	return err
}

func (s *Store) JobCancelRequested(ctx context.Context, id string) (bool, error) {
	var cancel bool
	if err := s.pool.QueryRow(ctx, `SELECT cancel_requested FROM jobs WHERE id=$1`, id).Scan(&cancel); err != nil {
		return false, mapNotFound(err)
	}
	return cancel, nil
}

func (s *Store) ListJobs(ctx context.Context, sessionID string, limit int) ([]domain.Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, jobSelect+` WHERE ($1='' OR session_id=$1) ORDER BY created_at LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		j, err := scanJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func scanJob(row pgx.Row) (*domain.Job, error) {
	var j domain.Job
	var lease, heartbeat *time.Time
	var payload string
	if err := row.Scan(&j.ID, &j.Kind, &j.SessionID, &j.PlanID, &j.State, &j.Attempts, &j.MaxAttempts,
		&j.LeasedBy, &lease, &heartbeat, &j.Error, &j.CancelRequested, &payload, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	if lease != nil {
		j.LeaseExpiresAt = *lease
	}
	if heartbeat != nil {
		j.HeartbeatAt = *heartbeat
	}
	j.Payload = json.RawMessage(payload)
	return &j, nil
}

func scanJobRows(rows pgx.Rows) (*domain.Job, error) {
	var j domain.Job
	var lease, heartbeat *time.Time
	var payload string
	if err := rows.Scan(&j.ID, &j.Kind, &j.SessionID, &j.PlanID, &j.State, &j.Attempts, &j.MaxAttempts,
		&j.LeasedBy, &lease, &heartbeat, &j.Error, &j.CancelRequested, &payload, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	if lease != nil {
		j.LeaseExpiresAt = *lease
	}
	if heartbeat != nil {
		j.HeartbeatAt = *heartbeat
	}
	j.Payload = json.RawMessage(payload)
	return &j, nil
}

// --- Event log and usage ---

func (s *Store) AppendEvent(ctx context.Context, e domain.Event) (int64, error) {
	payload, _ := json.Marshal(e.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var seq int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO events (id, session_id, type, payload, created_at) VALUES ($1,$2,$3,$4,$5) RETURNING seq`,
		e.ID, e.SessionID, e.Type, payload, ensureTime(e.CreatedAt)).Scan(&seq)
	return seq, err
}

func (s *Store) ListEvents(ctx context.Context, sessionID string, fromSeq int64) ([]domain.Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT seq, id, session_id, type, payload, created_at FROM events
		 WHERE session_id=$1 AND seq > $2 ORDER BY seq`, sessionID, fromSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var payload []byte
		if err := rows.Scan(&e.Seq, &e.ID, &e.SessionID, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) RecordUsage(ctx context.Context, u domain.Usage) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO usage_totals (session_id, prompt_tokens, completion_tokens, cost_usd)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (session_id) DO UPDATE SET
		   prompt_tokens = usage_totals.prompt_tokens + excluded.prompt_tokens,
		   completion_tokens = usage_totals.completion_tokens + excluded.completion_tokens,
		   cost_usd = usage_totals.cost_usd + excluded.cost_usd`,
		u.SessionID, u.PromptTokens, u.CompletionTokens, u.CostUSD)
	return err
}

func (s *Store) TotalUsage(ctx context.Context, sessionID string) (domain.Usage, error) {
	var u domain.Usage
	u.SessionID = sessionID
	err := s.pool.QueryRow(ctx,
		`SELECT prompt_tokens, completion_tokens, cost_usd FROM usage_totals WHERE session_id=$1`, sessionID).
		Scan(&u.PromptTokens, &u.CompletionTokens, &u.CostUSD)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, nil
	}
	return u, err
}

func ensureTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	tt := t.UTC()
	return &tt
}
