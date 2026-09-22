// Package store persists runs, nodes, events and artifacts.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// staleHeartbeat is how long a run may go without a heartbeat before another
// process is allowed to mark it interrupted.
const staleHeartbeat = 90 * time.Second

// ErrInvalidID is returned when an identifier is not safe to use as a path or
// key component.
var ErrInvalidID = errors.New("invalid identifier")

// ValidateID reports whether s is a safe identifier for run, node and artifact
// names. It permits letters, digits, dot, dash and underscore, forbids path
// separators and traversal, and caps the length.
func ValidateID(s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty", ErrInvalidID)
	}
	if len(s) > 200 {
		return fmt.Errorf("%w: too long", ErrInvalidID)
	}
	if s == "." || s == ".." || strings.Contains(s, "..") {
		return fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%w: %q", ErrInvalidID, s)
		}
	}
	return nil
}

// Store is the persistence layer.
type Store struct {
	db   *sql.DB
	root string
}

// Run summarizes one pipeline execution.
type Run struct {
	ID               string            `json:"id"`
	SessionID        string            `json:"session_id,omitempty"`
	Pipeline         string            `json:"pipeline"`
	Task             string            `json:"task"`
	Workspace        string            `json:"workspace"`
	Status           string            `json:"status"`
	Error            string            `json:"error,omitempty"`
	CostUSD          float64           `json:"cost_usd"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at,omitempty"`
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
	ID      int64          `json:"id"`
	RunID   string         `json:"run_id"`
	Type    string         `json:"type"`
	NodeID  string         `json:"node_id,omitempty"`
	Role    string         `json:"role,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Time    time.Time      `json:"time"`
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
	// Only mark runs whose owner has stopped heartbeating; a concurrently
	// running process keeps refreshing its heartbeat.
	_ = s.MarkStaleInterrupted()
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Root returns the artifact root directory.
func (s *Store) Root() string { return s.root }

// migrations are applied in order; each entry's index+1 is its schema version.
var migrations = []func(*sql.DB) error{
	func(db *sql.DB) error { // v1: base schema
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
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
		return nil
	},
	func(db *sql.DB) error { // v2: heartbeat + indexes
		stmts := []string{
			`ALTER TABLE runs ADD COLUMN pid INTEGER DEFAULT 0`,
			`ALTER TABLE runs ADD COLUMN host TEXT DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN heartbeat_at TEXT DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_events_run ON events(run_id, id)`,
			`CREATE INDEX IF NOT EXISTS idx_nodes_run ON nodes(run_id, started_at)`,
			`CREATE INDEX IF NOT EXISTS idx_artifacts_run ON artifacts(run_id, created_at)`,
		}
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil && !isDuplicateColumn(err) {
				return err
			}
		}
		return nil
	},
	func(db *sql.DB) error { // v3: persisted run inputs
		_, err := db.Exec(`ALTER TABLE runs ADD COLUMN inputs_json TEXT DEFAULT '{}'`)
		if err != nil && !isDuplicateColumn(err) {
			return err
		}
		return nil
	},
	func(db *sql.DB) error { // v4: event payloads
		_, err := db.Exec(`ALTER TABLE events ADD COLUMN data_json TEXT DEFAULT ''`)
		if err != nil && !isDuplicateColumn(err) {
			return err
		}
		return nil
	},
	func(db *sql.DB) error { // v5: sessions and chat messages
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS sessions (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL DEFAULT '',
				pipeline TEXT NOT NULL DEFAULT '',
				workspace TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL DEFAULT '',
				updated_at TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS messages (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL,
				run_id TEXT NOT NULL DEFAULT '',
				role TEXT NOT NULL DEFAULT '',
				model TEXT NOT NULL DEFAULT '',
				kind TEXT NOT NULL DEFAULT 'text',
				content TEXT NOT NULL DEFAULT '',
				data_json TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id)`,
			`ALTER TABLE runs ADD COLUMN session_id TEXT DEFAULT ''`,
		}
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil && !isDuplicateColumn(err) {
				return err
			}
		}
		return nil
	},
}

func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	version := 0
	row := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`)
	var raw string
	if err := row.Scan(&raw); err == nil {
		fmt.Sscanf(raw, "%d", &version)
	}
	for i := version; i < len(migrations); i++ {
		if err := migrations[i](s.db); err != nil {
			return fmt.Errorf("migrate v%d: %w", i+1, err)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%d", len(migrations)))
	return err
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
	if err := ValidateID(r.ID); err != nil {
		return err
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	host, _ := os.Hostname()
	inputs, _ := json.Marshal(r.Inputs)
	if r.Inputs == nil {
		inputs = []byte("{}")
	}
	_, err := s.db.Exec(
		`INSERT INTO runs (id, pipeline, task, workspace, status, error, started_at, finished_at, pid, host, heartbeat_at, inputs_json, session_id)
		 VALUES (?, ?, ?, ?, ?, '', ?, '', ?, ?, ?, ?, ?)`,
		r.ID, r.Pipeline, r.Task, r.Workspace, "running", formatTime(r.StartedAt),
		os.Getpid(), host, formatTime(time.Now()), string(inputs), r.SessionID,
	)
	return err
}

// Heartbeat refreshes a running run's liveness marker.
func (s *Store) Heartbeat(id string) error {
	_, err := s.db.Exec(`UPDATE runs SET heartbeat_at = ? WHERE id = ?`, formatTime(time.Now()), id)
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
	host, _ := os.Hostname()
	_, err := s.db.Exec(
		`UPDATE runs SET status = 'running', error = '', finished_at = '',
		        pid = ?, host = ?, heartbeat_at = ? WHERE id = ?`,
		os.Getpid(), host, formatTime(time.Now()), id)
	return err
}

// SetStatus updates a run's status without touching timing or error fields.
func (s *Store) SetStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE runs SET status = ? WHERE id = ?`, status, id)
	return err
}

// RevertFrom deletes the node record with the given id and every node that
// started at or after it, together with the events and artifacts produced from
// that point, so that a subsequent resume re-runs them cleanly.
func (s *Store) RevertFrom(runID, nodeID string) error {
	var started string
	row := s.db.QueryRow(`SELECT started_at FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	if err := row.Scan(&started); err != nil {
		return fmt.Errorf("node %s not found in run %s", nodeID, runID)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM nodes WHERE run_id = ? AND started_at >= ?`, runID, started); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE run_id = ? AND time >= ?`, runID, started); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM artifacts WHERE run_id = ? AND created_at >= ?`, runID, started); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkStaleInterrupted marks runs that are still "running" but whose owner has
// not sent a heartbeat recently. This avoids a second process (e.g. `runs
// list`) marking a concurrently running pipeline as interrupted.
func (s *Store) MarkStaleInterrupted() error {
	cutoff := formatTime(time.Now().Add(-staleHeartbeat))
	_, err := s.db.Exec(
		`UPDATE runs SET status = 'interrupted', finished_at = ?
		 WHERE status = 'running'
		   AND (finished_at IS NULL OR finished_at = '')
		   AND (heartbeat_at IS NULL OR heartbeat_at = '' OR heartbeat_at < ?)`,
		formatTime(time.Now()), cutoff,
	)
	return err
}

// SaveNode upserts a node record.
func (s *Store) SaveNode(n NodeRecord) error {
	if err := ValidateID(n.RunID); err != nil {
		return err
	}
	if err := ValidateID(n.NodeID); err != nil {
		return err
	}
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

// AppendEvent stores a progress event. data is persisted as JSON so the
// timeline can be faithfully replayed after a reload.
func (s *Store) AppendEvent(runID, typ, nodeID, role, message string, data map[string]any) error {
	dataJSON := ""
	if len(data) > 0 {
		if b, err := json.Marshal(data); err == nil {
			dataJSON = string(b)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO events (run_id, type, node_id, role, message, data_json, time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, typ, nodeID, role, message, dataJSON, formatTime(time.Now()),
	)
	return err
}

// SaveArtifact writes artifact content to disk and records it.
func (s *Store) SaveArtifact(runID, name, content string) (string, error) {
	if err := ValidateID(runID); err != nil {
		return "", fmt.Errorf("artifact: %w", err)
	}
	dir := filepath.Join(s.root, "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe := sanitize(name)
	path := filepath.Join(dir, safe)
	// Defense in depth: the resolved path must stay inside the run directory.
	if !within(dir, path) {
		return "", fmt.Errorf("artifact %q escapes run directory", name)
	}
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
		        COALESCE(cost_usd,0), COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
		        COALESCE(inputs_json,'{}'), COALESCE(session_id,'')
		 FROM runs WHERE id = ?`, id)
	var r Run
	var started, finished, inputs string
	if err := row.Scan(&r.ID, &r.Pipeline, &r.Task, &r.Workspace, &r.Status, &r.Error,
		&started, &finished, &r.CostUSD, &r.PromptTokens, &r.CompletionTokens, &inputs, &r.SessionID); err != nil {
		return nil, err
	}
	r.StartedAt = parseTime(started)
	r.FinishedAt = parseTime(finished)
	_ = json.Unmarshal([]byte(inputs), &r.Inputs)
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
		`SELECT id, run_id, type, node_id, role, message, COALESCE(data_json,''), time
		 FROM events WHERE run_id = ? ORDER BY id ASC LIMIT ?`,
		runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var ts, data string
		if err := rows.Scan(&e.ID, &e.RunID, &e.Type, &e.NodeID, &e.Role, &e.Message, &data, &ts); err != nil {
			return nil, err
		}
		if data != "" {
			_ = json.Unmarshal([]byte(data), &e.Data)
		}
		e.Time = parseTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListRecentEvents returns up to limit of the most recent events for a run, in
// chronological order. Unlike ListEvents it keeps the tail, which is what a
// live timeline needs.
func (s *Store) ListRecentEvents(runID string, limit int) ([]EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, run_id, type, node_id, role, message, COALESCE(data_json,''), time
		 FROM events WHERE run_id = ? ORDER BY id DESC LIMIT ?`,
		runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var ts, data string
		if err := rows.Scan(&e.ID, &e.RunID, &e.Type, &e.NodeID, &e.Role, &e.Message, &data, &ts); err != nil {
			return nil, err
		}
		if data != "" {
			_ = json.Unmarshal([]byte(data), &e.Data)
		}
		e.Time = parseTime(ts)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
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

// formatTime renders a fixed-width UTC timestamp so that lexicographic string
// comparisons (e.g. in RevertFrom) match chronological order.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// within reports whether path is inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	s := string(out)
	// Reject traversal-only or hidden names; collapse leading dots.
	for strings.HasPrefix(s, ".") {
		s = "_" + s[1:]
	}
	if s == "" || s == "_" {
		return "artifact"
	}
	return s
}
