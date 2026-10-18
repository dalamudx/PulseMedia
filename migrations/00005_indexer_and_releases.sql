-- +goose Up
-- M1-05: Indexer, Query Deduplication & Release Candidates

CREATE TABLE IF NOT EXISTS indexer (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    api_key_encrypted BYTEA NOT NULL,
    categories JSONB NOT NULL DEFAULT '[]'::jsonb,
    config_version BIGINT NOT NULL DEFAULT 1,
    health TEXT NOT NULL CHECK (health IN ('online', 'offline', 'rate_limited', 'stale')) DEFAULT 'online',
    rate_limit_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE TABLE IF NOT EXISTS indexer_query (
    id UUID PRIMARY KEY,
    indexer_id UUID NOT NULL REFERENCES indexer(id) ON DELETE CASCADE,
    query_type TEXT NOT NULL CHECK (query_type IN ('search', 'rss')),
    keyword TEXT NOT NULL,
    page INT NOT NULL DEFAULT 0,
    config_version BIGINT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed')),
    results_count INT NOT NULL DEFAULT 0,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

-- Active query deduplication: only running queries for the same keyword, page and config_version are shared
CREATE UNIQUE INDEX IF NOT EXISTS indexer_query_active_uniq
ON indexer_query (indexer_id, keyword, page, config_version)
WHERE state = 'running';

CREATE TABLE IF NOT EXISTS release_candidate (
    id UUID PRIMARY KEY,
    indexer_id UUID NOT NULL REFERENCES indexer(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    guid TEXT NOT NULL,
    link_encrypted BYTEA NOT NULL,
    info_hash TEXT,
    size_bytes BIGINT NOT NULL,
    seeders INT NOT NULL DEFAULT 0,
    leechers INT NOT NULL DEFAULT 0,
    publish_date TIMESTAMPTZ,
    quality_profile JSONB NOT NULL DEFAULT '{}'::jsonb,
    matched_media_id UUID REFERENCES media(id) ON DELETE SET NULL,
    matched_unit_id UUID REFERENCES media_unit(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT release_candidate_indexer_guid_uniq UNIQUE (indexer_id, guid)
);

CREATE INDEX IF NOT EXISTS release_candidate_infohash_idx ON release_candidate (info_hash);
CREATE INDEX IF NOT EXISTS release_candidate_matched_unit_idx ON release_candidate (matched_unit_id);

-- +goose Down
DROP TABLE IF EXISTS release_candidate;
DROP TABLE IF EXISTS indexer_query;
DROP TABLE IF EXISTS indexer;
