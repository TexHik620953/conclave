package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/texhik/conclave/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	dbPath string
}

type SessionSummary struct {
	ID        string
	Title     string
	Mode      domain.Mode
	Model     string
	UpdatedAt time.Time
}

func DefaultPath() string {
	if p := os.Getenv("CONCLAVE_DB"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "conclave", "conclave.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "conclave", "conclave.db")
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db, dbPath: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  cwd TEXT NOT NULL,
  mode TEXT NOT NULL,
  model TEXT NOT NULL,
  effort TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_updated ON sessions(updated_at DESC);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  phase TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_session ON runs(session_id, updated_at DESC);
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT,
  run_id TEXT,
  type TEXT NOT NULL,
  data BLOB NOT NULL,
  created_at TEXT NOT NULL
);
`)
	return err
}

func (s *Store) SaveSession(ctx context.Context, sess *domain.Session) error {
	sess.UpdatedAt = time.Now()
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO sessions(id,title,cwd,mode,model,effort,data,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET title=excluded.title,cwd=excluded.cwd,mode=excluded.mode,
model=excluded.model,effort=excluded.effort,data=excluded.data,updated_at=excluded.updated_at`,
		sess.ID, sess.Title, sess.Cwd, string(sess.Mode), sess.Model, sess.Effort, data,
		sess.CreatedAt.Format(time.RFC3339Nano), sess.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) LoadSession(ctx context.Context, id string) (*domain.Session, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("session %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	var sess domain.Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM runs WHERE session_id=?`,
		`DELETE FROM events WHERE session_id=?`,
		`DELETE FROM sessions WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	// Usage may be initialized by a separate service; remove its records when available.
	_, _ = tx.ExecContext(ctx, `DELETE FROM usage_records WHERE session_id=?`, id)
	return tx.Commit()
}

func (s *Store) LatestSession(ctx context.Context, cwd string) (*domain.Session, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE cwd=? ORDER BY updated_at DESC LIMIT 1`, cwd).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sess domain.Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) ListSessions(ctx context.Context, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,mode,model,updated_at FROM sessions ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var x SessionSummary
		var mode, updated string
		if err := rows.Scan(&x.ID, &x.Title, &mode, &x.Model, &updated); err != nil {
			return nil, err
		}
		x.Mode = domain.Mode(mode)
		x.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) SaveRun(ctx context.Context, run *domain.Run) error {
	run.UpdatedAt = time.Now()
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO runs(id,session_id,phase,data,created_at,updated_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET phase=excluded.phase,data=excluded.data,updated_at=excluded.updated_at`,
		run.ID, run.SessionID, string(run.Phase), data,
		run.CreatedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) LoadRun(ctx context.Context, id string) (*domain.Run, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM runs WHERE id=?`, id).Scan(&data)
	if err != nil {
		return nil, err
	}
	var run domain.Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) ListRuns(ctx context.Context, sessionID string, limit int) ([]*domain.Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM runs WHERE session_id=? ORDER BY updated_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Run
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var run domain.Run
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, err
		}
		out = append(out, &run)
	}
	return out, rows.Err()
}

func (s *Store) LatestRun(ctx context.Context, sessionID string) (*domain.Run, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM runs WHERE session_id=? ORDER BY updated_at DESC LIMIT 1`, sessionID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var run domain.Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) AppendEvent(ctx context.Context, ev domain.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO events(session_id,run_id,type,data,created_at) VALUES(?,?,?,?,?)`,
		ev.SessionID, ev.RunID, ev.Type, data, ev.Time.Format(time.RFC3339Nano))
	return err
}

// ImportLegacy copies sessions for cwd from the old global database into this
// workspace database. It is a no-op when there is no legacy database.
func (s *Store) ImportLegacy(ctx context.Context, cwd string) (int, error) {
	legacyPath := DefaultPath()
	if filepath.Clean(legacyPath) == filepath.Clean(s.path()) {
		return 0, nil
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return 0, nil
	}
	db, err := sql.Open("sqlite", legacyPath+"?_pragma=busy_timeout(2000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT data FROM sessions WHERE cwd=? ORDER BY updated_at`, cwd)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return n, err
		}
		var sess domain.Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue
		}
		if err := s.SaveSession(ctx, &sess); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func (s *Store) path() string { return s.dbPath }
