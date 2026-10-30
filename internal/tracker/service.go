package tracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
)

type Service struct {
	db      *sql.DB
	project Project
	bus     *event.Bus
}

func (s *Service) SetBus(bus *event.Bus) { s.bus = bus }

func New(db *sql.DB, cwd string) (*Service, error) {
	s := &Service{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`ALTER TABLE tracker_tasks ADD COLUMN origin_session_id TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE tracker_tasks ADD COLUMN batch_id TEXT NOT NULL DEFAULT ''`)
	for _, col := range []string{
		`ALTER TABLE tracker_tasks ADD COLUMN requirements TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE tracker_tasks ADD COLUMN constraints TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE tracker_tasks ADD COLUMN verification TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE tracker_tasks ADD COLUMN out_of_scope TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE tracker_tasks ADD COLUMN complexity_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tracker_tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'work'`,
		`ALTER TABLE tracker_tasks ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = s.db.Exec(col)
	}
	p, err := s.ensureProject(context.Background(), DetectProject(cwd))
	if err != nil {
		return nil, err
	}
	s.project = p
	s.backfillResults()
	return s, nil
}

func (s *Service) Project() Project { return s.project }

func (s *Service) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tracker_projects (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  root TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tracker_tasks (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  number INTEGER NOT NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  requirements TEXT NOT NULL DEFAULT '[]',
  constraints TEXT NOT NULL DEFAULT '[]',
  verification TEXT NOT NULL DEFAULT '[]',
  out_of_scope TEXT NOT NULL DEFAULT '[]',
  complexity INTEGER NOT NULL DEFAULT 3,
  complexity_reason TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 3,
  status TEXT NOT NULL,
  component TEXT NOT NULL DEFAULT '',
  labels TEXT NOT NULL DEFAULT '[]',
  epic TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'work',
  parent_id TEXT NOT NULL DEFAULT '',
  depends_on TEXT NOT NULL DEFAULT '[]',
  write_scope TEXT NOT NULL DEFAULT '[]',
  acceptance TEXT NOT NULL DEFAULT '[]',
  created_by TEXT NOT NULL,
  origin_session_id TEXT NOT NULL DEFAULT '',
  assignee TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  batch_id TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  branch TEXT NOT NULL DEFAULT '',
  base_commit TEXT NOT NULL DEFAULT '',
  commit_hash TEXT NOT NULL DEFAULT '',
  claim_owner TEXT NOT NULL DEFAULT '',
  claim_until TEXT,
  failure_reason TEXT NOT NULL DEFAULT '',
  failure_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ready_at TEXT,
  UNIQUE(project_id, number)
);
CREATE INDEX IF NOT EXISTS tracker_tasks_project_status ON tracker_tasks(project_id,status,priority DESC,created_at);
CREATE INDEX IF NOT EXISTS tracker_tasks_updated ON tracker_tasks(project_id,updated_at DESC);
CREATE TABLE IF NOT EXISTS tracker_comments (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tracker_comments_task ON tracker_comments(task_id,created_at);
CREATE TABLE IF NOT EXISTS tracker_events (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, type TEXT NOT NULL, actor TEXT NOT NULL, data TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tracker_events_task ON tracker_events(task_id,created_at);
CREATE TABLE IF NOT EXISTS tracker_claims (
  task_id TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at TEXT NOT NULL, heartbeat_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_attempts (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_no INTEGER NOT NULL,
  agent_id TEXT NOT NULL DEFAULT '', worker TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT, branch TEXT NOT NULL DEFAULT '', commit_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS task_attempts_task ON task_attempts(task_id,attempt_no);
CREATE TABLE IF NOT EXISTS tracker_meta (
  key TEXT PRIMARY KEY, value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_logs (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT NOT NULL, seq INTEGER NOT NULL,
  level TEXT NOT NULL, kind TEXT NOT NULL, content TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS task_logs_attempt ON task_logs(attempt_id,seq);
CREATE TABLE IF NOT EXISTS task_blockers (
  task_id TEXT NOT NULL, kind TEXT NOT NULL, ref_id TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, resolved_at TEXT, PRIMARY KEY(task_id,kind,ref_id)
);
CREATE TABLE IF NOT EXISTS task_results (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT NOT NULL, attempt_no INTEGER NOT NULL,
  content TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT 'pending', feedback TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, decided_at TEXT, decided_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS task_results_task ON task_results(task_id,created_at DESC);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE tracker_tasks ADD COLUMN failure_reason TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE tracker_tasks ADD COLUMN failure_at TEXT`)
	_, _ = s.db.Exec(`ALTER TABLE tracker_tasks ADD COLUMN base_commit TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE task_results ADD COLUMN feedback TEXT NOT NULL DEFAULT ''`)
	// Older versions used a dedicated failed status. Keep the failure marker
	// visible, but failed tasks belong to Backlog.
	_, _ = s.db.Exec(`UPDATE tracker_tasks SET status='backlog',
		failure_reason=CASE WHEN failure_reason='' THEN 'failed after retries' ELSE failure_reason END,
		failure_at=COALESCE(failure_at,updated_at)
		WHERE status='failed'`)
	return nil
}

func (s *Service) ensureProject(ctx context.Context, p Project) (Project, error) {
	var existing Project
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,key,name,root,created_at FROM tracker_projects WHERE id=?`, p.ID).
		Scan(&existing.ID, &existing.Key, &existing.Name, &existing.Root, &created)
	if err == nil {
		existing.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Project{}, err
	}
	key := p.Key
	for i := 2; ; i++ {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tracker_projects WHERE key=?`, key).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return Project{}, err
		}
		key = fmt.Sprintf("%s%d", p.Key, i)
	}
	p.Key = key
	p.CreatedAt = time.Now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO tracker_projects(id,key,name,root,created_at) VALUES(?,?,?,?,?)`,
		p.ID, p.Key, p.Name, p.Root, p.CreatedAt.Format(time.RFC3339Nano))
	return p, err
}

func (s *Service) Create(ctx context.Context, in CreateInput, actor string) (*Task, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, fmt.Errorf("task title is required")
	}
	if in.Description == "" {
		in.Description = in.Title
	}
	if in.Complexity < 1 {
		in.Complexity = 3
	}
	if in.Complexity > 5 {
		in.Complexity = 5
	}
	if in.Priority < 1 {
		in.Priority = 3
	}
	if in.Priority > 5 {
		in.Priority = 5
	}
	if in.Status == "" {
		in.Status = StatusBacklog
	}
	if in.CreatedBy == "" {
		in.CreatedBy = actor
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var num int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM tracker_tasks WHERE project_id=?`, s.project.ID).Scan(&num); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%s-%d", s.project.Key, num)
	now := time.Now()
	var readyAt any
	if in.Status == StatusReady {
		readyAt = now.Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tracker_tasks(
		id,project_id,number,title,description,context,requirements,constraints,verification,out_of_scope,complexity,complexity_reason,priority,status,component,labels,epic,kind,parent_id,depends_on,write_scope,acceptance,created_by,origin_session_id,assignee,run_id,batch_id,base_commit,created_at,updated_at,ready_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, s.project.ID, num, in.Title, in.Description, in.Context, jsonList(in.Requirements), jsonList(in.Constraints), jsonList(in.Verification), jsonList(in.OutOfScope), in.Complexity, in.ComplexityReason, in.Priority, string(in.Status), in.Component,
		jsonList(in.Labels), in.Epic, kindDefault(in.Kind), in.ParentID, jsonList(in.DependsOn), jsonList(in.WriteScope), jsonList(in.Acceptance),
		in.CreatedBy, in.OriginSessionID, in.Assignee, in.RunID, in.BatchID, in.BaseCommit, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), readyAt)
	if err != nil {
		return nil, err
	}
	if err := insertEventTx(ctx, tx, id, "created", actor, string(in.Status)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	task, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Status == StatusReady {
		_ = s.RefreshDependencyBlockers(ctx, id)
		task, _ = s.Get(ctx, id)
	}
	return task, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tracker_tasks WHERE id=? AND project_id=?`, id, s.project.ID)
	t, err := scanTask(row)
	if err != nil {
		return nil, err
	}
	t.Blockers, _ = s.Blockers(ctx, id)
	return t, nil
}

func (s *Service) CountOpen(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracker_tasks WHERE project_id=? AND status NOT IN (?,?)`,
		s.project.ID, string(StatusDone), string(StatusCancelled)).Scan(&n)
	return n, err
}

func (s *Service) List(ctx context.Context, f Filter) ([]Task, error) {
	var where []string
	var args []any
	where = append(where, "project_id=?")
	args = append(args, s.project.ID)
	if len(f.Status) > 0 {
		ph := make([]string, len(f.Status))
		for i, st := range f.Status {
			ph[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(ph, ",")+")")
	}
	if f.Component != "" {
		where = append(where, "component=?")
		args = append(args, f.Component)
	}
	if f.Label != "" {
		where = append(where, "labels LIKE ?")
		args = append(args, `%"`+f.Label+`"%`)
	}
	if f.Epic != "" {
		where = append(where, "epic=?")
		args = append(args, f.Epic)
	}
	if f.Assignee != "" {
		where = append(where, "assignee=?")
		args = append(args, f.Assignee)
	}
	if f.Search != "" {
		q := "%" + f.Search + "%"
		where = append(where, "(title LIKE ? OR description LIKE ? OR context LIKE ?)")
		args = append(args, q, q, q)
	}
	if len(f.Complexity) > 0 {
		ph := make([]string, len(f.Complexity))
		for i, v := range f.Complexity {
			ph[i] = "?"
			args = append(args, v)
		}
		where = append(where, "complexity IN ("+strings.Join(ph, ",")+")")
	}
	if len(f.Priority) > 0 {
		ph := make([]string, len(f.Priority))
		for i, v := range f.Priority {
			ph[i] = "?"
			args = append(args, v)
		}
		where = append(where, "priority IN ("+strings.Join(ph, ",")+")")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args = append(args, limit, max(0, f.Offset))
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tracker_tasks WHERE `+
		strings.Join(where, " AND ")+` ORDER BY priority DESC, created_at ASC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.attachBlockers(ctx, out)
	return out, nil
}

func (s *Service) Update(ctx context.Context, id string, in CreateInput, actor string) (*Task, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Title) == "" {
		in.Title = t.Title
	}
	if in.Description == "" {
		in.Description = t.Description
	}
	if in.Complexity < 1 || in.Complexity > 5 {
		in.Complexity = t.Complexity
	}
	if in.Priority < 1 || in.Priority > 5 {
		in.Priority = t.Priority
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET title=?,description=?,context=?,requirements=?,constraints=?,verification=?,out_of_scope=?,complexity=?,complexity_reason=?,priority=?,
		component=?,labels=?,epic=?,kind=?,parent_id=?,depends_on=?,write_scope=?,acceptance=?,assignee=?,updated_at=? WHERE id=? AND project_id=?`,
		in.Title, in.Description, in.Context, jsonList(in.Requirements), jsonList(in.Constraints), jsonList(in.Verification), jsonList(in.OutOfScope), in.Complexity, in.ComplexityReason, in.Priority, in.Component,
		jsonList(in.Labels), in.Epic, kindDefault(in.Kind), in.ParentID, jsonList(in.DependsOn), jsonList(in.WriteScope), jsonList(in.Acceptance),
		in.Assignee, time.Now().Format(time.RFC3339Nano), id, s.project.ID)
	if err != nil {
		return nil, err
	}
	_ = s.addEvent(ctx, id, "updated", actor, "")
	_ = s.RefreshDependencyBlockers(ctx, id)
	return s.Get(ctx, id)
}

func (s *Service) Transition(ctx context.Context, id string, to TaskStatus, actor string) (*Task, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canTransition(t.Status, to) {
		return nil, fmt.Errorf("cannot move %s from %s to %s", id, t.Status, to)
	}
	now := time.Now()
	var readyAt any
	if to == StatusReady {
		readyAt = now.Format(time.RFC3339Nano)
	}
	assignee := t.Assignee
	if to == StatusReady || to == StatusBacklog {
		assignee = ""
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,assignee=?,updated_at=?,ready_at=COALESCE(?,ready_at),
		failure_reason=CASE WHEN ?='ready' THEN '' ELSE failure_reason END,
		failure_at=CASE WHEN ?='ready' THEN NULL ELSE failure_at END
		WHERE id=? AND project_id=?`,
		string(to), assignee, now.Format(time.RFC3339Nano), readyAt, string(to), string(to), id, s.project.ID)
	if err != nil {
		return nil, err
	}
	if to != StatusClaimed && to != StatusInProgress {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tracker_claims WHERE task_id=?`, id)
	}
	_ = s.addEvent(ctx, id, "transition", actor, string(t.Status)+" -> "+string(to))
	if t.Status == StatusReview {
		switch to {
		case StatusDone:
			s.decidePendingResults(ctx, id, "approved", actor, "")
		case StatusReady, StatusBacklog:
			s.decidePendingResults(ctx, id, "rejected", actor, "")
		}
	}
	if to == StatusReady {
		_ = s.RefreshDependencyBlockers(ctx, id)
	}
	s.refreshDependents(ctx, id)
	return s.Get(ctx, id)
}

func (s *Service) StartTask(ctx context.Context, id, owner string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,assignee=?,attempts=attempts+1,updated_at=? WHERE id=? AND project_id=?`,
		string(StatusInProgress), owner, now.Format(time.RFC3339Nano), id, s.project.ID)
	if err != nil {
		return err
	}
	return s.addEvent(ctx, id, "started", owner, "")
}

func (s *Service) Delete(ctx context.Context, id string) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == StatusClaimed || t.Status == StatusInProgress {
		return fmt.Errorf("task %s is running; cancel it before deleting", id)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM task_logs WHERE task_id=?`,
		`DELETE FROM task_attempts WHERE task_id=?`,
		`DELETE FROM tracker_comments WHERE task_id=?`,
		`DELETE FROM tracker_events WHERE task_id=?`,
		`DELETE FROM tracker_claims WHERE task_id=?`,
		`DELETE FROM tracker_tasks WHERE id=? AND project_id=?`,
	} {
		if q == `DELETE FROM tracker_tasks WHERE id=? AND project_id=?` {
			if _, err := tx.ExecContext(ctx, q, id, s.project.ID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) SetExecution(ctx context.Context, id, branch, commit, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET branch=?,commit_hash=?,run_id=?,updated_at=? WHERE id=? AND project_id=?`,
		branch, commit, runID, time.Now().Format(time.RFC3339Nano), id, s.project.ID)
	if err != nil {
		return err
	}
	return s.addEvent(ctx, id, "execution", "worker", branch)
}

func (s *Service) AddComment(ctx context.Context, id, author, body string) (*Comment, error) {
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("comment is empty")
	}
	c := &Comment{ID: domain.NewID("cmt"), TaskID: id, Author: author, Body: body, CreatedAt: time.Now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tracker_comments(id,task_id,author,body,created_at) VALUES(?,?,?,?,?)`,
		c.ID, c.TaskID, c.Author, c.Body, c.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	_ = s.addEvent(ctx, id, "comment", author, "")
	return c, nil
}

func (s *Service) Comments(ctx context.Context, id string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,author,body,created_at FROM tracker_comments WHERE task_id=? ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		var created string
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Author, &c.Body, &created); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) Events(ctx context.Context, id string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,type,actor,data,created_at FROM tracker_events WHERE task_id=? ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var created string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Type, &e.Actor, &e.Data, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) ClaimNext(ctx context.Context, owner string, slots []WorkerSlot, lease time.Duration) (*Task, error) {
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	_ = s.ExpireClaims(ctx)
	tasks, err := s.List(ctx, Filter{Status: []TaskStatus{StatusReady}})
	if err != nil {
		return nil, err
	}
	doneDeps, err := s.doneTaskSet(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if !eligible(t, slots) || !depsDone(t.DependsOn, doneDeps) {
			continue
		}
		now := time.Now()
		until := now.Add(lease)
		res, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,assignee=?,claim_owner=?,claim_until=?,attempts=attempts+1,updated_at=?
			WHERE id=? AND project_id=? AND status=?`,
			string(StatusClaimed), owner, owner, until.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			t.ID, s.project.ID, string(StatusReady))
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		_, _ = s.db.ExecContext(ctx, `INSERT INTO tracker_claims(task_id,owner,expires_at,heartbeat_at) VALUES(?,?,?,?)
			ON CONFLICT(task_id) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at,heartbeat_at=excluded.heartbeat_at`,
			t.ID, owner, until.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		_ = s.addEvent(ctx, t.ID, "claimed", owner, "")
		return s.Get(ctx, t.ID)
	}
	return nil, nil
}

func (s *Service) Heartbeat(ctx context.Context, id, owner string, lease time.Duration) error {
	until := time.Now().Add(lease).Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `UPDATE tracker_claims SET expires_at=?,heartbeat_at=? WHERE task_id=? AND owner=?`,
		until, time.Now().Format(time.RFC3339Nano), id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("claim not held by %s", owner)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET claim_until=? WHERE id=?`, until, id)
	return err
}

func (s *Service) ExpireClaims(ctx context.Context) error {
	now := time.Now().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,owner FROM tracker_claims WHERE expires_at < ?`, now)
	if err != nil {
		return err
	}
	var expired []struct{ id, owner string }
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err == nil {
			expired = append(expired, struct{ id, owner string }{id, owner})
		}
	}
	rows.Close()
	for _, e := range expired {
		_, _ = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,assignee='',claim_owner='',claim_until=NULL,updated_at=? WHERE id=? AND status IN (?,?)`,
			string(StatusReady), now, e.id, string(StatusClaimed), string(StatusInProgress))
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tracker_claims WHERE task_id=?`, e.id)
		_ = s.addEvent(ctx, e.id, "lease_expired", "queue", "")
	}
	return nil
}

func (s *Service) doneTaskSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tracker_tasks WHERE project_id=? AND status=?`, s.project.ID, string(StatusDone))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out, rows.Err()
}

func (s *Service) RecoverInterrupted(ctx context.Context, reason string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tracker_tasks WHERE project_id=? AND status IN (?,?)`, s.project.ID, string(StatusClaimed), string(StatusInProgress))
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	now := time.Now().Format(time.RFC3339Nano)
	for _, id := range ids {
		var attemptID string
		_ = s.db.QueryRowContext(ctx, `SELECT id FROM task_attempts WHERE task_id=? AND status='running' ORDER BY attempt_no DESC LIMIT 1`, id).Scan(&attemptID)
		_, _ = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,assignee='',claim_owner='',claim_until=NULL,updated_at=? WHERE id=?`, string(StatusReady), now, id)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tracker_claims WHERE task_id=?`, id)
		_, _ = s.db.ExecContext(ctx, `UPDATE task_attempts SET status='interrupted',finished_at=? WHERE task_id=? AND status='running'`, now, id)
		if attemptID != "" {
			s.Log(ctx, id, attemptID, "warn", "system", "Attempt interrupted: "+reason)
		}
		_ = s.addEvent(ctx, id, "recovered", "queue", reason)
		_ = s.RefreshDependencyBlockers(ctx, id)
	}
	return len(ids), nil
}

func (s *Service) SetMeta(ctx context.Context, key, value string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO tracker_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
}

func (s *Service) Meta(ctx context.Context, key string) string {
	var v string
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM tracker_meta WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Service) SetQueueRunning(ctx context.Context, running bool) {
	v := "0"
	if running {
		v = "1"
	}
	s.SetMeta(ctx, "queue_running", v)
}

func (s *Service) QueueRunning(ctx context.Context) bool {
	return s.Meta(ctx, "queue_running") == "1"
}

func (s *Service) SetDependencies(ctx context.Context, id string, deps []string, actor string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET depends_on=?,updated_at=? WHERE id=? AND project_id=?`,
		jsonList(deps), time.Now().Format(time.RFC3339Nano), id, s.project.ID)
	if err != nil {
		return err
	}
	if err := s.addEvent(ctx, id, "dependencies", actor, strings.Join(deps, ",")); err != nil {
		return err
	}
	return s.RefreshDependencyBlockers(ctx, id)
}

func (s *Service) AssignBatch(ctx context.Context, ids []string, batchID string) error {
	now := time.Now().Format(time.RFC3339Nano)
	for _, id := range ids {
		_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET batch_id=?,status=?,updated_at=?,ready_at=COALESCE(ready_at,?) WHERE id=? AND project_id=?`,
			batchID, string(StatusReady), now, now, id, s.project.ID)
		if err != nil {
			return err
		}
		_ = s.addEvent(ctx, id, "batch_assigned", "conclave", batchID)
		if err := s.RefreshDependencyBlockers(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ByBatch(ctx context.Context, batchID string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tracker_tasks WHERE project_id=? AND batch_id=? ORDER BY priority DESC,created_at`, s.project.ID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// FailToBacklog stores the failure marker and returns the task to Backlog. The
// checkpoint/worktree is intentionally left in place so an explicit retry can
// continue from the same point.
func (s *Service) FailToBacklog(ctx context.Context, id, actor, reason string) error {
	now := time.Now().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,failure_reason=?,failure_at=?,assignee='',updated_at=? WHERE id=? AND project_id=?`,
		string(StatusBacklog), reason, now, now, id, s.project.ID)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM tracker_claims WHERE task_id=?`, id)
	return s.addEvent(ctx, id, "failed", actor, reason)
}

func (s *Service) SetTaskStatus(ctx context.Context, id string, status TaskStatus, actor string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,updated_at=? WHERE id=? AND project_id=?`,
		string(status), time.Now().Format(time.RFC3339Nano), id, s.project.ID)
	if err != nil {
		return err
	}
	_ = s.addEvent(ctx, id, "transition", actor, string(status))
	if status == StatusReady || status == StatusBlocked {
		return s.RefreshDependencyBlockers(ctx, id)
	}
	return nil
}

func (s *Service) StartAttempt(ctx context.Context, taskID string, attemptNo int, agentID, worker, model string) (*Attempt, error) {
	a := &Attempt{ID: domain.NewID("att"), TaskID: taskID, AttemptNo: attemptNo, AgentID: agentID, Worker: worker, Model: model, Status: "running", StartedAt: time.Now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_attempts(id,task_id,attempt_no,agent_id,worker,model,status,started_at) VALUES(?,?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, a.AttemptNo, a.AgentID, a.Worker, a.Model, a.Status, a.StartedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Service) FinishAttempt(ctx context.Context, attemptID, status, branch, commit string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE task_attempts SET status=?,finished_at=?,branch=?,commit_hash=? WHERE id=?`,
		status, time.Now().Format(time.RFC3339Nano), branch, commit, attemptID)
}

func (s *Service) Log(ctx context.Context, taskID, attemptID, level, kind, content string) {
	if len(content) > 200_000 {
		content = content[:200_000] + "\n…[truncated]"
	}
	var seq int
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0)+1 FROM task_logs WHERE attempt_id=?`, attemptID).Scan(&seq)
	_, _ = s.db.ExecContext(ctx, `INSERT INTO task_logs(id,task_id,attempt_id,seq,level,kind,content,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		domain.NewID("log"), taskID, attemptID, seq, level, kind, content, time.Now().Format(time.RFC3339Nano))
}

func (s *Service) Attempts(ctx context.Context, taskID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,attempt_no,agent_id,worker,model,status,started_at,finished_at,branch,commit_hash FROM task_attempts WHERE task_id=? ORDER BY attempt_no`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var started string
		var finished sql.NullString
		if err := rows.Scan(&a.ID, &a.TaskID, &a.AttemptNo, &a.AgentID, &a.Worker, &a.Model, &a.Status, &started, &finished, &a.Branch, &a.Commit); err != nil {
			return nil, err
		}
		a.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if finished.Valid {
			a.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished.String)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) Logs(ctx context.Context, taskID, attemptID string) ([]LogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,attempt_id,seq,level,kind,content,created_at FROM task_logs WHERE task_id=? AND attempt_id=? ORDER BY seq`, taskID, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var l LogEntry
		var created string
		if err := rows.Scan(&l.ID, &l.TaskID, &l.AttemptID, &l.Seq, &l.Level, &l.Kind, &l.Content, &created); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, l)
	}
	return out, rows.Err()
}

func canTransition(from, to TaskStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusBacklog:
		return to == StatusReady || to == StatusCancelled
	case StatusReady:
		return to == StatusClaimed || to == StatusBacklog || to == StatusBlocked || to == StatusCancelled
	case StatusClaimed:
		return to == StatusInProgress || to == StatusReady || to == StatusBlocked || to == StatusCancelled
	case StatusInProgress:
		return to == StatusReview || to == StatusBlocked || to == StatusCancelled || to == StatusReady || to == StatusBacklog
	case StatusReview:
		return to == StatusDone || to == StatusReady || to == StatusBlocked
	case StatusBlocked:
		return to == StatusReady || to == StatusBacklog || to == StatusCancelled
	case StatusDone, StatusCancelled:
		return to == StatusReady
	}
	return false
}

func eligible(t Task, slots []WorkerSlot) bool {
	if len(slots) == 0 {
		return true
	}
	for _, w := range slots {
		if len(w.Complexities) > 0 {
			ok := false
			for _, c := range w.Complexities {
				if c == t.Complexity {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if len(w.Labels) > 0 && len(t.Labels) > 0 {
			ok := false
			for _, l := range t.Labels {
				for _, wl := range w.Labels {
					if l == wl {
						ok = true
					}
				}
			}
			if !ok {
				continue
			}
		}
		return true
	}
	return false
}

func depsDone(deps []string, done map[string]bool) bool {
	for _, d := range deps {
		if !done[d] {
			return false
		}
	}
	return true
}

func (s *Service) addEvent(ctx context.Context, taskID, typ, actor, data string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tracker_events(id,task_id,type,actor,data,created_at) VALUES(?,?,?,?,?,?)`,
		domain.NewID("evt"), taskID, typ, actor, data, time.Now().Format(time.RFC3339Nano))
	return err
}

func insertEventTx(ctx context.Context, tx *sql.Tx, taskID, typ, actor, data string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tracker_events(id,task_id,type,actor,data,created_at) VALUES(?,?,?,?,?,?)`,
		domain.NewID("evt"), taskID, typ, actor, data, time.Now().Format(time.RFC3339Nano))
	return err
}

func (s *Service) Blockers(ctx context.Context, taskID string) ([]Blocker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,kind,ref_id,reason,created_at,resolved_at FROM task_blockers WHERE task_id=? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Blocker
	for rows.Next() {
		var b Blocker
		var created string
		var resolved sql.NullString
		if err := rows.Scan(&b.TaskID, &b.Kind, &b.RefID, &b.Reason, &created, &resolved); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if resolved.Valid {
			v, _ := time.Parse(time.RFC3339Nano, resolved.String)
			b.ResolvedAt = &v
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) attachBlockers(ctx context.Context, tasks []Task) {
	if len(tasks) == 0 {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT task_id,kind,ref_id,reason,created_at FROM task_blockers WHERE resolved_at IS NULL ORDER BY created_at`)
	if err != nil {
		return
	}
	defer rows.Close()
	byTask := map[string][]Blocker{}
	for rows.Next() {
		var b Blocker
		var created string
		if rows.Scan(&b.TaskID, &b.Kind, &b.RefID, &b.Reason, &created) == nil {
			b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
			byTask[b.TaskID] = append(byTask[b.TaskID], b)
		}
	}
	for i := range tasks {
		tasks[i].Blockers = byTask[tasks[i].ID]
	}
}

// RefreshDependencyBlockers reconciles dependency blockers for one task. Tasks
// waiting on unfinished dependencies are moved to Blocked; when all blockers
// are resolved the task returns to Ready.
func (s *Service) RefreshDependencyBlockers(ctx context.Context, taskID string) error {
	t, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339Nano)
	var dependencyBlockers int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_blockers WHERE task_id=? AND kind='dependency' AND resolved_at IS NULL`, taskID).Scan(&dependencyBlockers)
	deps := map[string]bool{}
	for _, dep := range t.DependsOn {
		deps[dep] = true
		depTask, err := s.Get(ctx, dep)
		if err == nil && depTask.Status == StatusDone {
			_, _ = s.db.ExecContext(ctx, `UPDATE task_blockers SET resolved_at=? WHERE task_id=? AND kind='dependency' AND ref_id=? AND resolved_at IS NULL`, now, taskID, dep)
			continue
		}
		reason := "waiting for dependency " + dep
		if err == nil && depTask != nil {
			reason = "waiting for " + dep + ": " + depTask.Title
		}
		_, _ = s.db.ExecContext(ctx, `INSERT INTO task_blockers(task_id,kind,ref_id,reason,created_at) VALUES(?, 'dependency', ?, ?, ?)
			ON CONFLICT(task_id,kind,ref_id) DO UPDATE SET reason=excluded.reason,resolved_at=NULL`, taskID, dep, reason, now)
	}
	// Remove dependency blockers for dependencies that were removed from the task.
	rows, _ := s.db.QueryContext(ctx, `SELECT ref_id FROM task_blockers WHERE task_id=? AND kind='dependency' AND resolved_at IS NULL`, taskID)
	if rows != nil {
		var stale []string
		for rows.Next() {
			var ref string
			if rows.Scan(&ref) == nil && !deps[ref] {
				stale = append(stale, ref)
			}
		}
		rows.Close()
		for _, ref := range stale {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM task_blockers WHERE task_id=? AND kind='dependency' AND ref_id=?`, taskID, ref)
		}
	}
	var unresolved int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_blockers WHERE task_id=? AND resolved_at IS NULL`, taskID).Scan(&unresolved)
	if unresolved > 0 {
		if t.Status == StatusReady {
			_, _ = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,assignee='',claim_owner='',claim_until=NULL,updated_at=? WHERE id=? AND project_id=?`, string(StatusBlocked), now, taskID, s.project.ID)
			_, _ = s.db.ExecContext(ctx, `DELETE FROM tracker_claims WHERE task_id=?`, taskID)
			_ = s.addEvent(ctx, taskID, "blocked", "queue", "waiting for dependencies")
			s.publishBoard("task blocked by dependency: " + taskID)
		}
	} else if t.Status == StatusBlocked && (len(t.DependsOn) > 0 || dependencyBlockers > 0) {
		_, _ = s.db.ExecContext(ctx, `UPDATE tracker_tasks SET status=?,updated_at=? WHERE id=? AND project_id=?`, string(StatusReady), now, taskID, s.project.ID)
		_ = s.addEvent(ctx, taskID, "unblocked", "queue", "dependencies satisfied")
		s.publishBoard("task unblocked: " + taskID)
	}
	return nil
}

func (s *Service) RefreshAllDependencyBlockers(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tracker_tasks
		WHERE project_id=? AND status IN (?,?) AND depends_on NOT IN ('', '[]')`,
		s.project.ID, string(StatusReady), string(StatusBlocked))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := s.RefreshDependencyBlockers(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) refreshDependents(ctx context.Context, taskID string) {
	tasks, err := s.List(ctx, Filter{Limit: 1000})
	if err != nil {
		return
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if dep == taskID {
				_ = s.RefreshDependencyBlockers(ctx, t.ID)
				break
			}
		}
	}
}

func (s *Service) publishBoard(text string) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(domain.Event{Type: "board.changed", Text: text, Time: time.Now()})
}

func (s *Service) SaveResult(ctx context.Context, taskID, attemptID string, attemptNo int, content string) (*Result, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		content = "No assistant summary recorded."
	}
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(ctx, `UPDATE task_results SET verdict='rejected',decided_at=?,decided_by='superseded' WHERE task_id=? AND verdict='pending'`, now.Format(time.RFC3339Nano), taskID)
	res := &Result{ID: domain.NewID("res"), TaskID: taskID, AttemptID: attemptID, AttemptNo: attemptNo, Content: content, Verdict: "pending", CreatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_results(id,task_id,attempt_id,attempt_no,content,verdict,created_at) VALUES(?,?,?,?,?,?,?)`,
		res.ID, res.TaskID, res.AttemptID, res.AttemptNo, res.Content, res.Verdict, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) Results(ctx context.Context, taskID string) ([]Result, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,attempt_id,attempt_no,content,verdict,feedback,created_at,decided_at,decided_by
		FROM task_results WHERE task_id=? ORDER BY created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var created string
		var decided sql.NullString
		if err := rows.Scan(&r.ID, &r.TaskID, &r.AttemptID, &r.AttemptNo, &r.Content, &r.Verdict, &r.Feedback, &created, &decided, &r.DecidedBy); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if decided.Valid {
			v, _ := time.Parse(time.RFC3339Nano, decided.String)
			r.DecidedAt = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) decidePendingResults(ctx context.Context, taskID, verdict, actor, feedback string) {
	now := time.Now().Format(time.RFC3339Nano)
	_, _ = s.db.ExecContext(ctx, `UPDATE task_results SET verdict=?,decided_at=?,decided_by=?,
		feedback=CASE WHEN ?<>'' THEN ? ELSE feedback END WHERE task_id=? AND verdict='pending'`,
		verdict, now, actor, feedback, feedback, taskID)
}

func (s *Service) SetPendingResultFeedback(ctx context.Context, taskID, feedback string) {
	if strings.TrimSpace(feedback) == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE task_results SET feedback=? WHERE task_id=? AND verdict='pending'`, feedback, taskID)
}

func (s *Service) backfillResults() {
	rows, err := s.db.QueryContext(context.Background(), `SELECT id FROM tracker_tasks WHERE project_id=? AND status=? AND NOT EXISTS (SELECT 1 FROM task_results r WHERE r.task_id=tracker_tasks.id)`,
		s.project.ID, string(StatusReview))
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, taskID := range ids {
		var attemptID string
		var attemptNo int
		_ = s.db.QueryRowContext(context.Background(), `SELECT id,attempt_no FROM task_attempts WHERE task_id=? AND status='review' ORDER BY attempt_no DESC LIMIT 1`, taskID).Scan(&attemptID, &attemptNo)
		if attemptID == "" {
			continue
		}
		var content string
		_ = s.db.QueryRowContext(context.Background(), `SELECT content FROM task_logs WHERE task_id=? AND attempt_id=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, taskID, attemptID).Scan(&content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		_, _ = s.SaveResult(context.Background(), taskID, attemptID, attemptNo, content)
	}
}

const taskColumns = `id,project_id,number,title,description,context,requirements,constraints,verification,out_of_scope,complexity,complexity_reason,priority,status,component,labels,epic,kind,parent_id,depends_on,write_scope,acceptance,created_by,origin_session_id,assignee,run_id,batch_id,attempts,branch,base_commit,commit_hash,claim_owner,claim_until,failure_reason,failure_at,created_at,updated_at,ready_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (*Task, error) {
	var t Task
	var labels, deps, scope, acceptance, requirements, constraints, verification, outOfScope string
	var created, updated string
	var claimUntil, readyAt, failureAt sql.NullString
	err := row.Scan(&t.ID, &t.ProjectID, &t.Number, &t.Title, &t.Description, &t.Context, &requirements, &constraints, &verification, &outOfScope, &t.Complexity, &t.ComplexityReason, &t.Priority,
		&t.Status, &t.Component, &labels, &t.Epic, &t.Kind, &t.ParentID, &deps, &scope, &acceptance, &t.CreatedBy, &t.OriginSessionID, &t.Assignee,
		&t.RunID, &t.BatchID, &t.Attempts, &t.Branch, &t.BaseCommit, &t.Commit, &t.ClaimOwner, &claimUntil, &t.FailureReason, &failureAt, &created, &updated, &readyAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(requirements), &t.Requirements)
	_ = json.Unmarshal([]byte(constraints), &t.Constraints)
	_ = json.Unmarshal([]byte(verification), &t.Verification)
	_ = json.Unmarshal([]byte(outOfScope), &t.OutOfScope)
	_ = json.Unmarshal([]byte(labels), &t.Labels)
	_ = json.Unmarshal([]byte(deps), &t.DependsOn)
	_ = json.Unmarshal([]byte(scope), &t.WriteScope)
	_ = json.Unmarshal([]byte(acceptance), &t.Acceptance)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if claimUntil.Valid {
		v, _ := time.Parse(time.RFC3339Nano, claimUntil.String)
		t.ClaimUntil = &v
	}
	if readyAt.Valid {
		v, _ := time.Parse(time.RFC3339Nano, readyAt.String)
		t.ReadyAt = &v
	}
	if failureAt.Valid {
		v, _ := time.Parse(time.RFC3339Nano, failureAt.String)
		t.FailureAt = &v
	}
	return &t, nil
}

func jsonList(v []string) string {
	if v == nil {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func kindDefault(k string) string {
	if k == "" {
		return "work"
	}
	return k
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Service) SetBatchID(ctx context.Context, id, batchID, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tracker_tasks SET batch_id=?,run_id=?,updated_at=? WHERE id=? AND project_id=?`,
		batchID, runID, time.Now().Format(time.RFC3339Nano), id, s.project.ID)
	return err
}
