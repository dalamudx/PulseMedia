-- +goose Up
CREATE TABLE episode_candidate_review (
 id UUID PRIMARY KEY, actor_id UUID NOT NULL REFERENCES app_user(id), subscription_id UUID NOT NULL REFERENCES subscription(id),
 media_id UUID NOT NULL REFERENCES media(id), unit_id UUID NOT NULL REFERENCES media_unit(id), candidate_id UUID NOT NULL REFERENCES release_candidate(id),
 input_stamp TEXT NOT NULL CHECK(input_stamp ~ '^[0-9a-f]{64}$'), unit_key TEXT NOT NULL,
 unit_version BIGINT NOT NULL CHECK(unit_version>0), media_version BIGINT NOT NULL CHECK(media_version>0), intent_version BIGINT NOT NULL CHECK(intent_version>0),
 mapping_revision BIGINT NOT NULL CHECK(mapping_revision>=0), policy_revision BIGINT NOT NULL CHECK(policy_revision>0), root_generation BIGINT NOT NULL,
 source_version BIGINT NOT NULL CHECK(source_version>0), parser_version TEXT NOT NULL,
 reason TEXT NOT NULL CHECK(length(reason) BETWEEN 1 AND 240), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION pulse_episode_candidate_review_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'episode candidate review evidence is immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER episode_candidate_review_guard BEFORE UPDATE OR DELETE ON episode_candidate_review FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM episode_candidate_review) THEN RAISE EXCEPTION 'episode candidate review history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE episode_candidate_review;
DROP FUNCTION pulse_episode_candidate_review_guard();
