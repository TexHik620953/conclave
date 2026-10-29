package usage

import (
	"context"
	"database/sql"
	"time"
)

type Record struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	AttemptID       string    `json:"attempt_id,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	Source          string    `json:"source"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	CachedTokens    int       `json:"cached_tokens"`
	ReasoningTokens int       `json:"reasoning_tokens"`
	Requests        int       `json:"requests"`
	LastContext     int       `json:"last_context"`
	ContextWindow   int       `json:"context_window"`
	CreatedAt       time.Time `json:"created_at"`
}

type ModelAgg struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	CachedTokens  int    `json:"cached_tokens"`
	Reasoning     int    `json:"reasoning_tokens"`
	Requests      int    `json:"requests"`
	LastContext   int    `json:"last_context"`
	ContextWindow int    `json:"context_window"`
	Sessions      int    `json:"sessions"`
	Tasks         int    `json:"tasks"`
}

type SessionAgg struct {
	SessionID     string     `json:"session_id"`
	InputTokens   int        `json:"input_tokens"`
	OutputTokens  int        `json:"output_tokens"`
	Requests      int        `json:"requests"`
	LastContext   int        `json:"last_context"`
	ContextWindow int        `json:"context_window"`
	Models        []ModelAgg `json:"models"`
	TaskIDs       []string   `json:"task_ids"`
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) (*Service, error) {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS usage_records (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  attempt_id TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  source TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  requests INTEGER NOT NULL DEFAULT 1,
  last_context INTEGER NOT NULL DEFAULT 0,
  context_window INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS usage_records_model ON usage_records(provider,model);
CREATE INDEX IF NOT EXISTS usage_records_session ON usage_records(session_id,provider,model);
CREATE INDEX IF NOT EXISTS usage_records_task ON usage_records(task_id);
`)
	if err != nil {
		return nil, err
	}
	return &Service{db: db}, nil
}

func (s *Service) Add(ctx context.Context, r Record) error {
	if r.ID == "" {
		r.ID = time.Now().UTC().Format("20060102150405.000000000") + "-" + r.SessionID + "-" + r.Model
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_records(id,session_id,task_id,attempt_id,agent_id,provider,model,source,input_tokens,output_tokens,cached_tokens,reasoning_tokens,requests,last_context,context_window,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.SessionID, r.TaskID, r.AttemptID, r.AgentID, r.Provider, r.Model, r.Source,
		r.InputTokens, r.OutputTokens, r.CachedTokens, r.ReasoningTokens, r.Requests,
		r.LastContext, r.ContextWindow, r.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Service) ByModel(ctx context.Context) ([]ModelAgg, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider,model,
		SUM(input_tokens),SUM(output_tokens),SUM(cached_tokens),SUM(reasoning_tokens),SUM(requests),
		MAX(last_context),MAX(context_window),COUNT(DISTINCT session_id),COUNT(DISTINCT task_id)
		FROM usage_records GROUP BY provider,model ORDER BY SUM(input_tokens) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelAgg
	for rows.Next() {
		var m ModelAgg
		if err := rows.Scan(&m.Provider, &m.Model, &m.InputTokens, &m.OutputTokens, &m.CachedTokens, &m.Reasoning, &m.Requests, &m.LastContext, &m.ContextWindow, &m.Sessions, &m.Tasks); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) BySession(ctx context.Context) ([]SessionAgg, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,provider,model,
		SUM(input_tokens),SUM(output_tokens),SUM(requests),MAX(last_context),MAX(context_window)
		FROM usage_records GROUP BY session_id,provider,model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySession := map[string]*SessionAgg{}
	order := []string{}
	for rows.Next() {
		var sid string
		var m ModelAgg
		if err := rows.Scan(&sid, &m.Provider, &m.Model, &m.InputTokens, &m.OutputTokens, &m.Requests, &m.LastContext, &m.ContextWindow); err != nil {
			return nil, err
		}
		a := bySession[sid]
		if a == nil {
			a = &SessionAgg{SessionID: sid}
			bySession[sid] = a
			order = append(order, sid)
		}
		a.Models = append(a.Models, m)
		a.InputTokens += m.InputTokens
		a.OutputTokens += m.OutputTokens
		a.Requests += m.Requests
		if m.LastContext > a.LastContext {
			a.LastContext = m.LastContext
		}
		if m.ContextWindow > a.ContextWindow {
			a.ContextWindow = m.ContextWindow
		}
	}
	var out []SessionAgg
	for _, sid := range order {
		a := bySession[sid]
		trows, _ := s.db.QueryContext(ctx, `SELECT DISTINCT task_id FROM usage_records WHERE session_id=? AND task_id<>'' ORDER BY task_id`, sid)
		if trows != nil {
			for trows.Next() {
				var id string
				if trows.Scan(&id) == nil {
					a.TaskIDs = append(a.TaskIDs, id)
				}
			}
			trows.Close()
		}
		out = append(out, *a)
	}
	return out, nil
}

func (s *Service) ByTask(ctx context.Context, taskID string) ([]ModelAgg, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider,model,SUM(input_tokens),SUM(output_tokens),SUM(cached_tokens),SUM(reasoning_tokens),SUM(requests),MAX(last_context),MAX(context_window),0,1
		FROM usage_records WHERE task_id=? GROUP BY provider,model`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelAgg
	for rows.Next() {
		var m ModelAgg
		if err := rows.Scan(&m.Provider, &m.Model, &m.InputTokens, &m.OutputTokens, &m.CachedTokens, &m.Reasoning, &m.Requests, &m.LastContext, &m.ContextWindow, &m.Sessions, &m.Tasks); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
