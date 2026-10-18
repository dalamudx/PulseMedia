-- +goose Up
-- M2-04: Persistent SearchRound Snapshots, Deadlines, Evaluation & Invalidation Gates

CREATE TABLE IF NOT EXISTS search_round (
    id UUID PRIMARY KEY,
    subscription_id UUID NOT NULL REFERENCES subscription(id) ON DELETE CASCADE,
    wanted_item_id UUID NOT NULL REFERENCES wanted_item(id) ON DELETE CASCADE,
    snapshot_intent_version BIGINT NOT NULL,
    snapshot_policy_revision BIGINT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'completed', 'aborted', 'expired')) DEFAULT 'running',
    deadline TIMESTAMPTZ NOT NULL,
    candidate_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    selected_candidate_id UUID REFERENCES release_candidate(id) ON DELETE SET NULL,
    abort_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS search_round_wanted_idx ON search_round (wanted_item_id, state);
CREATE INDEX IF NOT EXISTS search_round_deadline_idx ON search_round (deadline) WHERE state = 'running';

-- +goose Down
DROP TABLE IF EXISTS search_round;
