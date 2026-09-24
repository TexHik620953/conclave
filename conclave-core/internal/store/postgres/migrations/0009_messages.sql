-- S3: assistant messages with streaming partial content.
CREATE TABLE IF NOT EXISTS messages (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    playbook_run_id TEXT NOT NULL DEFAULT '',
    node_id         TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT 'assistant',
    content         TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'streaming',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);
