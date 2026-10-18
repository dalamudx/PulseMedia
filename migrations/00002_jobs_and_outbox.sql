-- +goose Up
-- M1-01: Persistent Job Queue & Outbox Pattern

CREATE TABLE IF NOT EXISTS job (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL,
    dedupe_key TEXT,
    priority INT NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'dead')),
    payload JSONB,
    result JSONB,
    error TEXT,
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    not_before TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    lease_owner TEXT,
    lease_token BIGINT NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS job_dedupe
ON job (kind, dedupe_key)
WHERE dedupe_key IS NOT NULL AND state IN ('queued', 'running');

CREATE INDEX IF NOT EXISTS job_claim_idx
ON job (state, not_before, priority DESC, id)
WHERE state = 'queued';

CREATE INDEX IF NOT EXISTS job_lease_cleanup_idx
ON job (state, lease_until)
WHERE state = 'running';

CREATE TABLE IF NOT EXISTS outbox_event (
    id UUID PRIMARY KEY,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    seq BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

CREATE INDEX IF NOT EXISTS outbox_event_aggregate_idx
ON outbox_event (aggregate_type, aggregate_id, seq);

CREATE TABLE IF NOT EXISTS outbox_delivery (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES outbox_event(id) ON DELETE CASCADE,
    consumer TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    seq BIGINT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'delivering', 'delivered', 'failed', 'dead')),
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    not_before TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    lease_owner TEXT,
    lease_token BIGINT NOT NULL DEFAULT 0,
    lease_until TIMESTAMPTZ,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    delivered_at TIMESTAMPTZ,
    CONSTRAINT outbox_delivery_event_consumer_uniq UNIQUE (event_id, consumer)
);

CREATE INDEX IF NOT EXISTS outbox_delivery_claim_idx
ON outbox_delivery (consumer, state, not_before, seq)
WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS outbox_delivery_aggregate_seq_idx
ON outbox_delivery (consumer, aggregate_type, aggregate_id, seq);

-- +goose Down
DROP TABLE IF EXISTS outbox_delivery;
DROP TABLE IF EXISTS outbox_event;
DROP TABLE IF EXISTS job;
