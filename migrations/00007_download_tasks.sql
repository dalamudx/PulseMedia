-- +goose Up
-- M1-07: Download Clients, Tasks, Reconciliation & Path Mapping

CREATE TABLE IF NOT EXISTS download_client (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    client_type TEXT NOT NULL CHECK (client_type IN ('qbittorrent', 'transmission')),
    url TEXT NOT NULL,
    username TEXT NOT NULL,
    password_encrypted BYTEA NOT NULL,
    remote_path_map JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE TABLE IF NOT EXISTS download_task (
    id UUID PRIMARY KEY,
    operation_id UUID NOT NULL UNIQUE,
    client_id UUID NOT NULL REFERENCES download_client(id) ON DELETE RESTRICT,
    candidate_id UUID NOT NULL REFERENCES release_candidate(id) ON DELETE RESTRICT,
    info_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('submitting', 'tracking', 'completed', 'unknown', 'failed', 'canceled')),
    save_path TEXT NOT NULL,
    content_path TEXT,
    progress REAL NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    downloaded_bytes BIGINT NOT NULL DEFAULT 0,
    reconcile_count INT NOT NULL DEFAULT 0,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS download_task_infohash_idx ON download_task (info_hash);
CREATE INDEX IF NOT EXISTS download_task_state_idx ON download_task (state);

-- +goose Down
DROP TABLE IF EXISTS download_task;
DROP TABLE IF EXISTS download_client;
