// Package store persists runs, nodes, events and artifacts.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the persistence layer.
type Store struct {
	db   *sql.DB
	root string
}

// Run summarizes one pipeline execution.
type Run struct {
	ID               string    `json:"id"`
	Pipeline         string    `json:"pipeline"`
	Task             string    `json:"task"`
	Workspace        string    `json:"workspace"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitempty"`
	CostUSD          float64   `json:"cost_usd"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at,omitempty"`
}

// NodeRecord summarizes one node execution.
type NodeRecord struct {
	RunID      string    `json:"run_id"`
	NodeID     string    `json:"node_id"`
	Role       string    `json:"role,omitempty"`
	Status     string    `json:"status"`
	Prompt     string    `json:"prompt,omitempty"`
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Tokens     int       `json:"tokens,omitempty"`
}

// Artifact is a named output stored on disk.
type Artifact struct {
	RunID     string    `json:"run_id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

// EventRecord is a stored progress event.
type EventRecord struct {
	ID      int64     `json:"id"`
	RunID   string    `json:"run_id"`
	Type    string    `json:"type"`
	NodeID  string    `json:"node_id,omitempty"`
	Role    string    `json:"role,omitempty"`
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"time"`
}

// Open opens (creating if needed) the SQLite database and artifact root.
func Open(dbPath, root string) (*Store, error) {
	if dbPath == "" {
		dbPath = filepath.Join(root, "conclave.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; serialize access to avoid "database is
	// locked" errors when pipeline nodes run concurrently.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, root: root}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	_ = s.MarkInterrupted()
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Root returns the artifact root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			pipeline TEXT NOT NULL DEFAULT '',
			task TEXT NOT NULL DEFAULT '',
			workspace TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '',
			finished_at TEXT NOT NULL DEFAULT '',
			cost_usd REAL NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			run_id TEXT,
			node_id TEXT,
			role TEXT,
			status TEXT,
			prompt TEXT,
			output TEXT,
			error TEXT,
			started_at TEXT,
			finished_at TEXT,
			tokens INTEGER DEFAULT 0,
			PRIMARY KEY (run_id, node_id)
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT,
			type TEXT,
			node_id TEXT,
			role TEXT,
			message TEXT,
			time TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			run_id TEXT,
			name TEXT,
			path TEXT,
			created_at TEXT,
			PRIMARY KEY (run_id, name)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Best-effort column additions for databases created by older versions.
	for _, alter := range []string{
		`ALTER TABLE runs ADD COLUMN cost_usd REAL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN prompt_tokens INTEGER DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN completion_tokens INTEGER DEFAULT 0`,
		`ALTER TABLE nodes ADD COLUMN prompt TEXT`,
	} {
		_, _ = s.db.Exec(alter)
	}
	return nil
}

// UpdateRunUsage stores cost and token totals for a run.
func (s *Store) UpdateRunUsage(runID string, cost float64, promptTokens, completionTokens int) error {
	_, err := s.db.Exec(
		`UPDATE runs SET cost_usd = ?, prompt_tokens = ?, completion_tokens = ? WHERE id = ?`,
		cost, promptTokens, completionTokens, runID,
	)
	return err
}

// CreateRun inserts a new run row.
func (s *Store) CreateRun(r Run) error {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO runs (id, pipeline, task, workspace, status, error, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, '', ?, '')`,
		r.ID, r.Pipeline, r.Task, r.Workspace, "running", formatTime(r.StartedAt),
	)
	return err
}

// FinishRun marks a run complete.
func (s *Store) FinishRun(id, status, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, errMsg, formatTime(time.Now()), id,
	)
	return err
}

// MarkRunning marks a run as running again (used when resuming).
func (s *Store) MarkRunning(id string) error {
	_, err := s.db.Exec(
		`UPDATE runs SET status = 'running', error = '', finished_at = '' WHERE id = ?`, id)
	return err
}

// MarkInterrupted marks runs left in the running state (e.g. by a killed
// process) as interrupted.
func (s *Store) MarkInterrupted() error {
	_, err := s.db.Exec(
		`UPDATE runs SET status = 'interrupted', finished_at = ?
		 WHERE status = 'running' AND (finished_at IS NULL OR finished_at = '')`,
		formatTime(time.Now()),
	)
	return err
}

// SaveNode upserts a node record.
func (s *Store) SaveNode(n NodeRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO nodes (run_id, node_id, role, status, prompt, output, error, started_at, finished_at, tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, node_id) DO UPDATE SET
		   role=excluded.role, status=excluded.status, prompt=excluded.prompt,
		   output=excluded.output, error=excluded.error,
		   finished_at=excluded.finished_at, tokens=excluded.tokens`,
		n.RunID, n.NodeID, n.Role, n.Status, n.Prompt, n.Output, n.Error,
		formatTime(n.StartedAt), formatTime(n.FinishedAt), n.Tokens,
	)
	return err
}

// AppendEvent stores a progress event.
func (s *Store) AppendEvent(runID, typ, nodeID, role, message string) error {
	_, err := s.db.Exec(
		`INSERT INTO events (run_id, type, node_id, role, message, time) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, typ, nodeID, role, message, formatTime(time.Now()),
	)
	return err
}

// SaveArtifact writes artifact content to disk and records it.
func (s *Store) SaveArtifact(runID, name, content string) (string, error) {
	dir := filepath.Join(s.root, "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe := sanitize(name)
	path := filepath.Join(dir, safe)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	_, err := s.db.Exec(
		`INSERT INTO artifacts (run_id, name, path, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(run_id, name) DO UPDATE SET path=excluded.path, created_at=excluded.created_at`,
		runID, name, path, formatTime(time.Now()),
	)
	if err != nil {
		return "", err
	}
	return path, nil
}

// GetRun fetches a run by id.
func (s *Store) GetRun(id string) (*Run, error) {
	row := s.db.QueryRow(
		`SELECT id, COALESCE(pipeline,''), COALESCE(task,''), COALESCE(workspace,''),
		        COALESCE(status,''), COALESCE(error,''), COALESCE(started_at,''), COALESCE(finished_at,''),
		        COALESCE(cost_usd,0), COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0)
		 FROM runs WHERE id = ?`, id)
	var r Run
	var started, finished string
	if err := row.Scan(&r.ID, &r.Pipeline, &r.Task, &r.Workspace, &r.Status, &r.Error,
		&started, &finished, &r.CostUSD, &r.PromptTokens, &r.CompletionTokens); err != nil {
		return nil, err
	}
	r.StartedAt = parseTime(started)
	r.FinishedAt = parseTime(finished)
	return &r, nil
}

// ListRuns returns recent runs.
func (s *Store) ListRuns(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT id, COALESCE(pipeline,''), COALESCE(task,''), COALESCE(workspace,''),
		        COALESCE(status,''), COALESCE(error,''), COALESCE(started_at,''), COALESCE(finished_at,''),
		        COALESCE(cost_usd,0), COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0)
		 FROM runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var started, finished string
		if err := rows.Scan(&r.ID, &r.Pipeline, &r.Task, &r.Workspace, &r.Status, &r.Error,
			&started, &finished, &r.CostUSD, &r.PromptTokens, &r.CompletionTokens); err != nil {
			return nil, err
		}
		r.StartedAt = parseTime(started)
		r.FinishedAt = parseTime(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListNodes returns node records for a run.
func (s *Store) ListNodes(runID string) ([]NodeRecord, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, COALESCE(role,''), COALESCE(status,''), COALESCE(prompt,''), COALESCE(output,''),
		        COALESCE(error,''), COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(tokens,0)
		 FROM nodes WHERE run_id = ? ORDER BY started_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRecord
	for rows.Next() {
		var n NodeRecord
		var started, finished string
		if err := rows.Scan(&n.RunID, &n.NodeID, &n.Role, &n.Status, &n.Prompt, &n.Output, &n.Error, &started, &finished, &n.Tokens); err != nil {
			return nil, err
		}
		n.StartedAt = parseTime(started)
		n.FinishedAt = parseTime(finished)
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListEvents returns stored events for a run in chronological order.
func (s *Store) ListEvents(runID string, limit int) ([]EventRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(
		`SELECT id, run_id, type, node_id, role, message, time FROM events WHERE run_id = ? ORDER BY id ASC LIMIT ?`,
		runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var ts string
		if err := rows.Scan(&e.ID, &e.RunID, &e.Type, &e.NodeID, &e.Role, &e.Message, &ts); err != nil {
			return nil, err
		}
		e.Time = parseTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListArtifacts returns artifacts for a run.
func (s *Store) ListArtifacts(runID string) ([]Artifact, error) {
	rows, err := s.db.Query(
		`SELECT run_id, COALESCE(name,''), COALESCE(path,''), COALESCE(created_at,'') FROM artifacts WHERE run_id = ? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var created string
		if err := rows.Scan(&a.RunID, &a.Name, &a.Path, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "artifact"
	}
	return string(out)
}
