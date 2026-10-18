-- +goose Up
CREATE TABLE candidate_rule_cache(input_digest TEXT PRIMARY KEY CHECK(input_digest ~ '^[a-f0-9]{64}$'),decision JSONB NOT NULL CHECK(jsonb_typeof(decision)='object' AND decision->>'decision' IN ('accept','reject','hold')),created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp());
-- Pure result cache only. No availability, root state, URLs or raw adapter data.
-- +goose Down
DROP TABLE candidate_rule_cache;
