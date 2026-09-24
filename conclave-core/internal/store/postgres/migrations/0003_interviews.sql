-- P2: interviews and typed questions.

ALTER TABLE questions ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'gate';
ALTER TABLE questions ADD COLUMN IF NOT EXISTS ref TEXT NOT NULL DEFAULT '';
UPDATE questions SET ref = playbook_run_id WHERE ref = '' AND playbook_run_id <> '';

CREATE TABLE IF NOT EXISTS interviews (
    id                  TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    idea                TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL DEFAULT 'running',
    pending_question_id TEXT NOT NULL DEFAULT '',
    transcript          JSONB NOT NULL DEFAULT '[]',
    spec_id             TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_interviews_session ON interviews(session_id);
