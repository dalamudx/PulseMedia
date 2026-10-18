-- +goose Up
CREATE TABLE episode_execution_authority (
 operation_id UUID PRIMARY KEY, review_id UUID NOT NULL REFERENCES episode_candidate_review(id),
 subscription_id UUID NOT NULL REFERENCES subscription(id), candidate_id UUID NOT NULL REFERENCES release_candidate(id),
 unit_id UUID NOT NULL REFERENCES media_unit(id), root_id UUID NOT NULL REFERENCES library_root(id),
 client_id UUID NOT NULL REFERENCES download_client(id), actor_id UUID NOT NULL REFERENCES app_user(id),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION pulse_episode_execution_authority_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'episode execution authority is immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER episode_execution_authority_guard BEFORE UPDATE OR DELETE ON episode_execution_authority FOR EACH ROW EXECUTE FUNCTION pulse_episode_execution_authority_guard();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM episode_execution_authority) THEN RAISE EXCEPTION 'episode execution history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE episode_execution_authority;
DROP FUNCTION pulse_episode_execution_authority_guard();
