-- S4: bind a session to the device whose agent executes its tools.
CREATE TABLE IF NOT EXISTS session_agents (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    device_id  TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_session_agents_device ON session_agents(device_id);
