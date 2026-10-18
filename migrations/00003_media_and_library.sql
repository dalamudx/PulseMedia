-- +goose Up
-- M1-02: Media Identity, Unit, Library File Versioning, Multi-Unit Mapping & Fact Constraints

CREATE TABLE IF NOT EXISTS media (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    original_title TEXT,
    year INT,
    media_type TEXT NOT NULL CHECK (media_type IN ('movie', 'tv')),
    metadata_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE TABLE IF NOT EXISTS media_identity (
    id UUID PRIMARY KEY,
    media_id UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    namespace TEXT NOT NULL,
    external_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT media_identity_ns_ext_uniq UNIQUE (namespace, external_id)
);

CREATE INDEX IF NOT EXISTS media_identity_media_idx ON media_identity (media_id);

CREATE TABLE IF NOT EXISTS media_unit (
    id UUID PRIMARY KEY,
    media_id UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    unit_key TEXT NOT NULL,
    air_date DATE,
    retired BOOLEAN NOT NULL DEFAULT false,
    facts_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT media_unit_media_key_uniq UNIQUE (media_id, unit_key)
);

CREATE TABLE IF NOT EXISTS library_root (
    id UUID PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    media_type TEXT NOT NULL CHECK (media_type IN ('movie', 'tv')),
    name TEXT NOT NULL,
    health TEXT NOT NULL CHECK (health IN ('online', 'offline', 'stale', 'stalled')) DEFAULT 'online',
    generation BIGINT NOT NULL DEFAULT 1,
    scan_token BIGINT NOT NULL DEFAULT 0,
    mutation_seq BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE TABLE IF NOT EXISTS library_file (
    id UUID PRIMARY KEY,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
    rel_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    mtime_ns BIGINT NOT NULL,
    file_fingerprint TEXT,
    last_mutation_seq BIGINT NOT NULL DEFAULT 0,
    retired_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- Active file unique index: only one active file per root and relative path
CREATE UNIQUE INDEX IF NOT EXISTS library_file_current_path
ON library_file (root_id, rel_path)
WHERE retired_at IS NULL;

CREATE INDEX IF NOT EXISTS library_file_root_mutation_idx
ON library_file (root_id, last_mutation_seq);

-- Multi-unit mapping: one file can map to one or multiple units (e.g. multi-episode files)
CREATE TABLE IF NOT EXISTS library_file_unit (
    file_id UUID NOT NULL REFERENCES library_file(id) ON DELETE CASCADE,
    unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (file_id, unit_id)
);

CREATE INDEX IF NOT EXISTS library_file_unit_unit_idx ON library_file_unit (unit_id);

CREATE TABLE IF NOT EXISTS unit_fact (
    id UUID PRIMARY KEY,
    unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
    stage TEXT NOT NULL CHECK (stage IN ('submitted', 'downloaded', 'imported', 'confirmed')),
    source_file_id UUID REFERENCES library_file(id) ON DELETE SET NULL,
    source_task_id UUID,
    root_id UUID REFERENCES library_root(id) ON DELETE RESTRICT,
    quality_profile JSONB NOT NULL DEFAULT '{}'::jsonb,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    -- Enforce fact origin constraints: imported/confirmed must point to source file
    CONSTRAINT unit_fact_source_check CHECK (
        (stage IN ('imported', 'confirmed') AND source_file_id IS NOT NULL) OR
        (stage IN ('submitted', 'downloaded'))
    )
);

CREATE INDEX IF NOT EXISTS unit_fact_unit_stage_idx ON unit_fact (unit_id, stage) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS unit_fact_file_idx ON unit_fact (source_file_id);

-- +goose Down
DROP TABLE IF EXISTS unit_fact;
DROP TABLE IF EXISTS library_file_unit;
DROP TABLE IF EXISTS library_file;
DROP TABLE IF EXISTS library_root;
DROP TABLE IF EXISTS media_unit;
DROP TABLE IF EXISTS media_identity;
DROP TABLE IF EXISTS media;
