-- +goose Up
-- M0 Baseline Schema: User authentication, sessions, API tokens, settings, audit, idempotency

CREATE TABLE IF NOT EXISTS app_user (
    id UUID PRIMARY KEY,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE UNIQUE INDEX IF NOT EXISTS app_user_lower_username ON app_user (LOWER(username));

CREATE TABLE IF NOT EXISTS app_session (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    csrf_token_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE INDEX IF NOT EXISTS app_session_user_idx ON app_session (user_id);
CREATE INDEX IF NOT EXISTS app_session_expires_idx ON app_session (expires_at);

CREATE TABLE IF NOT EXISTS api_token (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE,
    scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE INDEX IF NOT EXISTS api_token_user_idx ON api_token (user_id);

CREATE TABLE IF NOT EXISTS setting (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE TABLE IF NOT EXISTS audit_log (
    id UUID PRIMARY KEY,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    payload JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE INDEX IF NOT EXISTS audit_log_created_idx ON audit_log (created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx ON audit_log (actor_type, actor_id);

CREATE TABLE IF NOT EXISTS idempotency_record (
    id UUID PRIMARY KEY,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_code INT NOT NULL,
    response_body JSONB,
    resource_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT idempotency_record_key_uniq UNIQUE (actor_type, actor_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idempotency_record_created_idx ON idempotency_record (created_at);

-- +goose Down
DROP TABLE IF EXISTS idempotency_record;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS setting;
DROP TABLE IF EXISTS api_token;
DROP TABLE IF EXISTS app_session;
DROP TABLE IF EXISTS app_user;
