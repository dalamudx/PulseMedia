-- +goose Up
-- M1-09: Media Server Integration, Refresh Deliveries & Path Mapping

CREATE TABLE IF NOT EXISTS media_server (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    server_type TEXT NOT NULL CHECK (server_type IN ('jellyfin', 'emby', 'plex')),
    url TEXT NOT NULL,
    api_key_encrypted BYTEA NOT NULL,
    path_mapping JSONB NOT NULL DEFAULT '{}'::jsonb,
    health TEXT NOT NULL CHECK (health IN ('online', 'offline', 'error')) DEFAULT 'online',
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- +goose Down
DROP TABLE IF EXISTS media_server;
