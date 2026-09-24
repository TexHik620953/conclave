-- Separate internal node id from the user-facing node key used by plan edges.
ALTER TABLE plan_nodes ADD COLUMN IF NOT EXISTS node_key TEXT NOT NULL DEFAULT '';
UPDATE plan_nodes SET node_key = id WHERE node_key = '';
