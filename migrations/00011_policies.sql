-- +goose Up
-- M2-03: Quality & Filtering Policies, Immutable Revisions & Reject Lists

CREATE TABLE IF NOT EXISTS policy (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    current_revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE TABLE IF NOT EXISTS policy_revision (
    id UUID PRIMARY KEY,
    policy_id TEXT NOT NULL REFERENCES policy(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    rules JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT policy_revision_uniq UNIQUE (policy_id, revision)
);

CREATE INDEX IF NOT EXISTS policy_revision_lookup_idx ON policy_revision (policy_id, revision);

-- Seed default policy and initial revision 1
INSERT INTO policy (id, name, current_revision, created_at, updated_at)
VALUES ('default', 'Default Movie Quality Policy', 1, statement_timestamp(), statement_timestamp())
ON CONFLICT (id) DO NOTHING;

INSERT INTO policy_revision (id, policy_id, revision, rules, created_at)
VALUES (
    '018e0000-0000-7000-8000-000000000001',
    'default',
    1,
    '{
        "cutoff_score": 300,
        "prefer_hdr": true,
        "resolution_order": {"2160p": 400, "1080p": 300, "720p": 200, "480p": 100},
        "source_order": {"Remux": 50, "Bluray": 40, "WEB-DL": 30, "WEBRip": 20, "HDTV": 10},
        "reject_terms": ["cam", "ts", "telesync", "hdrip", "dvdrip"],
        "reject_groups": []
    }'::jsonb,
    statement_timestamp()
)
ON CONFLICT (policy_id, revision) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS policy_revision;
DROP TABLE IF EXISTS policy;
