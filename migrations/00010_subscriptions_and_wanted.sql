-- +goose Up
-- M2-01: Subscriptions, Intent Versioning, Scopes & Wanted Items

CREATE TABLE IF NOT EXISTS subscription (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    media_id UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
    quality_policy_id TEXT NOT NULL DEFAULT 'default',
    quality_policy_revision BIGINT NOT NULL DEFAULT 1,
    indexer_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    downloader_id UUID REFERENCES download_client(id) ON DELETE SET NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'paused', 'archived')) DEFAULT 'active',
    intent_version BIGINT NOT NULL DEFAULT 1,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('all', 'selected')) DEFAULT 'all',
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT subscription_user_media_root_uniq UNIQUE (user_id, media_id, root_id)
);

CREATE INDEX IF NOT EXISTS subscription_user_idx ON subscription (user_id);
CREATE INDEX IF NOT EXISTS subscription_state_idx ON subscription (state);

CREATE TABLE IF NOT EXISTS wanted_item (
    id UUID PRIMARY KEY,
    subscription_id UUID NOT NULL REFERENCES subscription(id) ON DELETE CASCADE,
    unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE CASCADE,
    intent_version BIGINT NOT NULL,
    is_skipped BOOLEAN NOT NULL DEFAULT false,
    current_fact_stage TEXT CHECK (current_fact_stage IN ('submitted', 'downloaded', 'imported', 'confirmed')),
    is_satisfied BOOLEAN NOT NULL DEFAULT false,
    last_evaluated_at TIMESTAMPTZ,
    next_evaluate_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT wanted_item_sub_unit_uniq UNIQUE (subscription_id, unit_id)
);

CREATE INDEX IF NOT EXISTS wanted_item_sub_idx ON wanted_item (subscription_id);
CREATE INDEX IF NOT EXISTS wanted_item_eval_idx ON wanted_item (is_satisfied, is_skipped, next_evaluate_at);

-- +goose Down
DROP TABLE IF EXISTS wanted_item;
DROP TABLE IF EXISTS subscription;
