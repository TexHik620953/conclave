-- Offload interviews and planning to jobs: generic job payload.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS payload_json TEXT NOT NULL DEFAULT '';
