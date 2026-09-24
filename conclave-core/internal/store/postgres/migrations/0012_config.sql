-- Configuration is stored in the database and hot-reloaded by every replica.
-- A trigger bumps config_version and notifies listeners on any change.

CREATE TABLE IF NOT EXISTS providers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    base_url    TEXT NOT NULL DEFAULT '',
    api_key     TEXT NOT NULL DEFAULT '',
    headers     JSONB NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS provider_models (
    id                  TEXT PRIMARY KEY,
    provider_id         TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    display_name        TEXT NOT NULL DEFAULT '',
    context_window      INTEGER NOT NULL DEFAULT 0,
    input_price_per_1m  DOUBLE PRECISION NOT NULL DEFAULT 0,
    output_price_per_1m DOUBLE PRECISION NOT NULL DEFAULT 0,
    enabled             BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_id, name)
);
CREATE INDEX IF NOT EXISTS idx_provider_models_provider ON provider_models(provider_id);

CREATE TABLE IF NOT EXISTS roles (
    id          TEXT PRIMARY KEY,
    key         TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    control     TEXT NOT NULL DEFAULT 'supervisor',
    guidelines  TEXT NOT NULL DEFAULT '',
    inputs      JSONB NOT NULL DEFAULT '[]',
    outputs     JSONB NOT NULL DEFAULT '[]',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS role_grades (
    id            TEXT PRIMARY KEY,
    role_id       TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    grade         TEXT NOT NULL,
    rank          INTEGER NOT NULL DEFAULT 0,
    model_id      TEXT NOT NULL REFERENCES provider_models(id) ON DELETE RESTRICT,
    tools         JSONB NOT NULL DEFAULT '[]',
    system_prompt TEXT NOT NULL DEFAULT '',
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (role_id, grade)
);
CREATE INDEX IF NOT EXISTS idx_role_grades_role ON role_grades(role_id);

CREATE TABLE IF NOT EXISTS brain_configs (
    name          TEXT PRIMARY KEY,
    model_id      TEXT NOT NULL REFERENCES provider_models(id) ON DELETE RESTRICT,
    system_prompt TEXT NOT NULL DEFAULT '',
    max_steps     INTEGER NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS config_version (
    id         INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    version    BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO config_version (id, version) VALUES (1, 1) ON CONFLICT DO NOTHING;

ALTER TABLE plan_nodes ADD COLUMN IF NOT EXISTS role_id TEXT NOT NULL DEFAULT '';
ALTER TABLE plan_nodes ADD COLUMN IF NOT EXISTS grade TEXT NOT NULL DEFAULT '';
UPDATE plan_nodes SET role_id = playbook_id WHERE role_id = '' AND playbook_id <> '';

CREATE OR REPLACE FUNCTION conclave_bump_config_version() RETURNS trigger AS $$
BEGIN
    UPDATE config_version SET version = version + 1, updated_at = now();
    PERFORM pg_notify('conclave_config', '');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_config_providers ON providers;
CREATE TRIGGER trg_config_providers AFTER INSERT OR UPDATE OR DELETE ON providers
    FOR EACH STATEMENT EXECUTE FUNCTION conclave_bump_config_version();

DROP TRIGGER IF EXISTS trg_config_provider_models ON provider_models;
CREATE TRIGGER trg_config_provider_models AFTER INSERT OR UPDATE OR DELETE ON provider_models
    FOR EACH STATEMENT EXECUTE FUNCTION conclave_bump_config_version();

DROP TRIGGER IF EXISTS trg_config_roles ON roles;
CREATE TRIGGER trg_config_roles AFTER INSERT OR UPDATE OR DELETE ON roles
    FOR EACH STATEMENT EXECUTE FUNCTION conclave_bump_config_version();

DROP TRIGGER IF EXISTS trg_config_role_grades ON role_grades;
CREATE TRIGGER trg_config_role_grades AFTER INSERT OR UPDATE OR DELETE ON role_grades
    FOR EACH STATEMENT EXECUTE FUNCTION conclave_bump_config_version();

DROP TRIGGER IF EXISTS trg_config_brain_configs ON brain_configs;
CREATE TRIGGER trg_config_brain_configs AFTER INSERT OR UPDATE OR DELETE ON brain_configs
    FOR EACH STATEMENT EXECUTE FUNCTION conclave_bump_config_version();
