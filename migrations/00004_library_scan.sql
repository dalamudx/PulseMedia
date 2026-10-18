-- +goose Up
-- M1-03: Library Root Probe, Scan Runs, Observation Batches & Mutation Sequence Gates

ALTER TABLE library_root
ADD COLUMN IF NOT EXISTS marker_filename TEXT NOT NULL DEFAULT '.pulsemedia_root',
ADD COLUMN IF NOT EXISTS baseline_completed BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS scan_run (
    id UUID PRIMARY KEY,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE CASCADE,
    scan_token BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    start_mutation_seq BIGINT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'finalizing', 'succeeded', 'failed', 'partial')),
    files_observed BIGINT NOT NULL DEFAULT 0,
    files_added BIGINT NOT NULL DEFAULT 0,
    files_updated BIGINT NOT NULL DEFAULT 0,
    files_missing_candidates BIGINT NOT NULL DEFAULT 0,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS scan_run_root_idx ON scan_run (root_id, state);

CREATE TABLE IF NOT EXISTS scan_observation (
    id UUID PRIMARY KEY,
    scan_run_id UUID NOT NULL REFERENCES scan_run(id) ON DELETE CASCADE,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE CASCADE,
    rel_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    mtime_ns BIGINT NOT NULL,
    fingerprint TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE INDEX IF NOT EXISTS scan_obs_run_idx ON scan_observation (scan_run_id, rel_path);

-- +goose Down
DROP TABLE IF EXISTS scan_observation;
DROP TABLE IF EXISTS scan_run;
ALTER TABLE library_root
DROP COLUMN IF EXISTS baseline_completed,
DROP COLUMN IF EXISTS marker_filename;
