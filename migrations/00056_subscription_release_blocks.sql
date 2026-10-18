-- +goose Up
CREATE TABLE release_block (
 subscription_id UUID NOT NULL REFERENCES subscription(id) ON DELETE RESTRICT,
 candidate_id UUID NOT NULL REFERENCES release_candidate(id) ON DELETE RESTRICT,
 blocked BOOLEAN NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(subscription_id,candidate_id)
);

-- +goose Down
-- +goose StatementBegin
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM release_block) THEN RAISE EXCEPTION 'release block history prevents downgrade'; END IF;
END$$;
-- +goose StatementEnd
DROP TABLE release_block;
