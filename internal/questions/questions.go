package questions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/tracker"
)

type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type Question struct {
	ID          string   `json:"id"`
	Text        string   `json:"text"`
	Type        string   `json:"type"` // single | multiple | text
	AllowCustom bool     `json:"allow_custom"`
	Required    bool     `json:"required"`
	Options     []Option `json:"options,omitempty"`
	Selected    []string `json:"selected,omitempty"`
	CustomText  string   `json:"custom_text,omitempty"`
}

type AnswerSet struct {
	SetID      string              `json:"set_id"`
	Answers    map[string]Question `json:"answers"`
	AnsweredBy string              `json:"answered_by"`
	Cancelled  bool                `json:"cancelled"`
}

type Set struct {
	ID           string     `json:"id"`
	SessionID    string     `json:"session_id,omitempty"`
	TaskID       string     `json:"task_id,omitempty"`
	AttemptID    string     `json:"attempt_id,omitempty"`
	AgentID      string     `json:"agent_id,omitempty"`
	Title        string     `json:"title"`
	Category     string     `json:"category"`
	Status       string     `json:"status"`
	TaskContext  string     `json:"task_context,omitempty"`
	AgentContext string     `json:"agent_context,omitempty"`
	Questions    []Question `json:"questions"`
	CreatedAt    time.Time  `json:"created_at"`
	AnsweredAt   *time.Time `json:"answered_at,omitempty"`
	AnsweredBy   string     `json:"answered_by,omitempty"`
}

type Service struct {
	db      *sql.DB
	tracker *tracker.Service
	bus     *event.Bus
	mu      sync.Mutex
	waiters map[string]chan AnswerSet
}

func New(db *sql.DB, tr *tracker.Service, bus *event.Bus) (*Service, error) {
	s := &Service{db: db, tracker: tr, bus: bus, waiters: map[string]chan AnswerSet{}}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS question_sets (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '', attempt_id TEXT NOT NULL DEFAULT '', agent_id TEXT NOT NULL DEFAULT '',
 title TEXT NOT NULL, category TEXT NOT NULL DEFAULT 'clarification', status TEXT NOT NULL,
 task_context TEXT NOT NULL DEFAULT '', agent_context TEXT NOT NULL DEFAULT '', data TEXT NOT NULL, created_at TEXT NOT NULL, answered_at TEXT,
 answered_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS question_sets_status ON question_sets(status,created_at);
CREATE TABLE IF NOT EXISTS task_blockers (
 task_id TEXT NOT NULL, kind TEXT NOT NULL, ref_id TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, resolved_at TEXT,
 PRIMARY KEY(task_id,kind,ref_id)
);
`)
	if err != nil {
		return err
	}
	// Older databases do not have this column yet.
	_, _ = s.db.Exec(`ALTER TABLE question_sets ADD COLUMN answered_by TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *Service) Create(ctx context.Context, set Set, autonomous bool) (*Set, error) {
	if len(set.Questions) == 0 {
		return nil, fmt.Errorf("question set is empty")
	}
	if set.ID == "" {
		set.ID = domain.NewID("q")
	}
	if set.Status == "" {
		if autonomous {
			set.Status = "pending_human"
		} else {
			set.Status = "pending_answer"
		}
	}
	if set.Title == "" {
		set.Title = set.Questions[0].Text
	}
	if set.Category == "" {
		set.Category = "clarification"
	}
	set.CreatedAt = time.Now()
	data, _ := json.Marshal(set.Questions)
	_, err := s.db.ExecContext(ctx, `INSERT INTO question_sets(id,session_id,task_id,attempt_id,agent_id,title,category,status,task_context,agent_context,data,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		set.ID, set.SessionID, set.TaskID, set.AttemptID, set.AgentID, set.Title, set.Category, set.Status, set.TaskContext, set.AgentContext, data, set.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if autonomous && set.TaskID != "" && s.tracker != nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO task_blockers(task_id,kind,ref_id,reason,created_at) VALUES(?,?,?,?,?) ON CONFLICT(task_id,kind,ref_id) DO NOTHING`,
			set.TaskID, "question", set.ID, set.Title, set.CreatedAt.Format(time.RFC3339Nano))
		_ = s.tracker.SetTaskStatus(ctx, set.TaskID, tracker.StatusBlocked, "question")
	}
	s.publish(set, "question.created")
	return &set, nil
}

func (s *Service) Delete(ctx context.Context, setID string) error {
	set, err := s.Get(ctx, setID)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM question_sets WHERE id=?`, setID)
	if set.TaskID != "" {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM task_blockers WHERE task_id=? AND kind='question' AND ref_id=?`, set.TaskID, setID)
		if s.tracker != nil && s.unblocked(ctx, set.TaskID) {
			_ = s.tracker.SetTaskStatus(ctx, set.TaskID, tracker.StatusReady, "question")
		}
	}
	s.mu.Lock()
	if ch := s.waiters[setID]; ch != nil {
		ch <- AnswerSet{SetID: setID, Cancelled: true}
	}
	s.mu.Unlock()
	if s.bus != nil {
		s.bus.Publish(domain.Event{Time: time.Now(), SessionID: set.SessionID, TaskID: set.TaskID, Type: "question.deleted", Text: set.Title, Payload: set})
	}
	return nil
}

func (s *Service) Wait(ctx context.Context, set *Set) (AnswerSet, error) {
	ch := make(chan AnswerSet, 1)
	s.mu.Lock()
	s.waiters[set.ID] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, set.ID); s.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return AnswerSet{}, ctx.Err()
	case a := <-ch:
		return a, nil
	}
}

func (s *Service) Answer(ctx context.Context, setID string, answers map[string]Question, by string) (*Set, error) {
	set, err := s.Get(ctx, setID)
	if err != nil {
		return nil, err
	}
	for i := range set.Questions {
		if a, ok := answers[set.Questions[i].ID]; ok {
			set.Questions[i].Selected = a.Selected
			set.Questions[i].CustomText = a.CustomText
		}
		if set.Questions[i].Required && len(set.Questions[i].Selected) == 0 && strings.TrimSpace(set.Questions[i].CustomText) == "" {
			return nil, fmt.Errorf("question %q is required", set.Questions[i].ID)
		}
	}
	now := time.Now()
	set.Status = "answered"
	set.AnsweredAt = &now
	set.AnsweredBy = by
	data, _ := json.Marshal(set.Questions)
	_, err = s.db.ExecContext(ctx, `UPDATE question_sets SET status=?,data=?,answered_at=?,answered_by=? WHERE id=?`, set.Status, data, now.Format(time.RFC3339Nano), by, set.ID)
	if err != nil {
		return nil, err
	}
	if set.TaskID != "" && s.tracker != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE task_blockers SET resolved_at=? WHERE task_id=? AND kind='question' AND ref_id=?`, now.Format(time.RFC3339Nano), set.TaskID, set.ID)
		if s.unblocked(ctx, set.TaskID) {
			_ = s.tracker.SetTaskStatus(ctx, set.TaskID, tracker.StatusReady, "question")
		}
	}
	s.publish(*set, "question.answered")
	s.mu.Lock()
	if ch := s.waiters[set.ID]; ch != nil {
		ch <- AnswerSet{SetID: set.ID, Answers: answers, AnsweredBy: by}
	}
	s.mu.Unlock()
	return set, nil
}

func (s *Service) unblocked(ctx context.Context, taskID string) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_blockers WHERE task_id=? AND resolved_at IS NULL`, taskID).Scan(&n)
	return n == 0
}

func (s *Service) Get(ctx context.Context, id string) (*Set, error) {
	var set Set
	var data, created string
	var answered sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,session_id,task_id,attempt_id,agent_id,title,category,status,task_context,agent_context,data,created_at,answered_at,answered_by FROM question_sets WHERE id=?`, id).
		Scan(&set.ID, &set.SessionID, &set.TaskID, &set.AttemptID, &set.AgentID, &set.Title, &set.Category, &set.Status, &set.TaskContext, &set.AgentContext, &data, &created, &answered, &set.AnsweredBy)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(data), &set.Questions)
	set.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if answered.Valid {
		t, _ := time.Parse(time.RFC3339Nano, answered.String)
		set.AnsweredAt = &t
	}
	return &set, nil
}

func (s *Service) List(ctx context.Context, status string) ([]Set, error) {
	q := `SELECT id FROM question_sets`
	args := []any{}
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Set
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			if set, err := s.Get(ctx, id); err == nil {
				out = append(out, *set)
			}
		}
	}
	return out, rows.Err()
}

func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM question_sets WHERE status IN ('pending_human','pending_answer','pending_master')`).Scan(&n)
	return n, err
}

func (s *Service) publish(set Set, typ string) {
	if s.bus != nil {
		s.bus.Publish(domain.Event{Time: time.Now(), SessionID: set.SessionID, TaskID: set.TaskID, Type: typ, Text: set.Title, Payload: set})
	}
}
