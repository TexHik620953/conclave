package store

import (
	"encoding/json"
	"time"
)

// Session is a chat conversation that groups runs and their messages.
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Pipeline  string    `json:"pipeline"`
	Workspace string    `json:"workspace"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message is one entry in a session's conversation log.
type Message struct {
	ID        int64          `json:"id"`
	SessionID string         `json:"session_id"`
	RunID     string         `json:"run_id,omitempty"`
	Role      string         `json:"role"`
	Model     string         `json:"model,omitempty"`
	Kind      string         `json:"kind"` // text | tool_call | tool_result | question | error | user
	Content   string         `json:"content,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// CreateSession inserts a new session.
func (s *Store) CreateSession(sess Session) error {
	if err := ValidateID(sess.ID); err != nil {
		return err
	}
	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = now
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, title, pipeline, workspace, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.Title, sess.Pipeline, sess.Workspace, formatTime(sess.CreatedAt), formatTime(sess.UpdatedAt),
	)
	return err
}

// TouchSession updates a session's title (when non-empty) and updated_at.
func (s *Store) TouchSession(id, title string) error {
	if title != "" {
		_, err := s.db.Exec(`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`,
			title, formatTime(time.Now()), id)
		return err
	}
	_, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(time.Now()), id)
	return err
}

// GetSession fetches a session by id.
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, COALESCE(title,''), COALESCE(pipeline,''), COALESCE(workspace,''),
		        COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM sessions WHERE id = ?`, id)
	var sess Session
	var created, updated string
	if err := row.Scan(&sess.ID, &sess.Title, &sess.Pipeline, &sess.Workspace, &created, &updated); err != nil {
		return nil, err
	}
	sess.CreatedAt = parseTime(created)
	sess.UpdatedAt = parseTime(updated)
	return &sess, nil
}

// ListSessions returns recent sessions, newest first.
func (s *Store) ListSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, COALESCE(title,''), COALESCE(pipeline,''), COALESCE(workspace,''),
		        COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM sessions ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		var created, updated string
		if err := rows.Scan(&sess.ID, &sess.Title, &sess.Pipeline, &sess.Workspace, &created, &updated); err != nil {
			return nil, err
		}
		sess.CreatedAt = parseTime(created)
		sess.UpdatedAt = parseTime(updated)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteSession removes a session and its messages.
func (s *Store) DeleteSession(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendMessage stores a conversation message and bumps the session timestamp.
func (s *Store) AppendMessage(m Message) (int64, error) {
	dataJSON := ""
	if len(m.Data) > 0 {
		if b, err := json.Marshal(m.Data); err == nil {
			dataJSON = string(b)
		}
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	res, err := s.db.Exec(
		`INSERT INTO messages (session_id, run_id, role, model, kind, content, data_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.SessionID, m.RunID, m.Role, m.Model, m.Kind, m.Content, dataJSON, formatTime(m.CreatedAt),
	)
	if err != nil {
		return 0, err
	}
	if m.SessionID != "" {
		_, _ = s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(m.CreatedAt), m.SessionID)
	}
	return res.LastInsertId()
}

// ListMessages returns up to limit messages for a session, oldest first.
func (s *Store) ListMessages(sessionID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(
		`SELECT id, session_id, COALESCE(run_id,''), COALESCE(role,''), COALESCE(model,''),
		        COALESCE(kind,'text'), COALESCE(content,''), COALESCE(data_json,''), COALESCE(created_at,'')
		 FROM messages WHERE session_id = ? ORDER BY id ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var data, created string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.RunID, &m.Role, &m.Model, &m.Kind, &m.Content, &data, &created); err != nil {
			return nil, err
		}
		if data != "" {
			_ = json.Unmarshal([]byte(data), &m.Data)
		}
		m.CreatedAt = parseTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListRunsBySession returns runs belonging to a session, oldest first.
func (s *Store) ListRunsBySession(sessionID string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, COALESCE(pipeline,''), COALESCE(task,''), COALESCE(workspace,''),
		        COALESCE(status,''), COALESCE(error,''), COALESCE(started_at,''), COALESCE(finished_at,''),
		        COALESCE(cost_usd,0), COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
		        COALESCE(inputs_json,'{}'), COALESCE(session_id,'')
		 FROM runs WHERE session_id = ? ORDER BY started_at ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var started, finished, inputs string
		if err := rows.Scan(&r.ID, &r.Pipeline, &r.Task, &r.Workspace, &r.Status, &r.Error,
			&started, &finished, &r.CostUSD, &r.PromptTokens, &r.CompletionTokens, &inputs, &r.SessionID); err != nil {
			return nil, err
		}
		r.StartedAt = parseTime(started)
		r.FinishedAt = parseTime(finished)
		_ = json.Unmarshal([]byte(inputs), &r.Inputs)
		out = append(out, r)
	}
	return out, rows.Err()
}
