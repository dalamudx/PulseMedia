-- +goose Up
CREATE TABLE tv_coverage_candidate_review (
 id UUID PRIMARY KEY,
 actor_id UUID NOT NULL REFERENCES app_user(id),
 subscription_id UUID NOT NULL REFERENCES subscription(id),
 candidate_id UUID NOT NULL REFERENCES release_candidate(id),
 input_stamp TEXT NOT NULL CHECK(input_stamp ~ '^[0-9a-f]{64}$'),
 material_stamp TEXT NOT NULL CHECK(material_stamp ~ '^[0-9a-f]{64}$'),
 material JSONB NOT NULL CHECK(jsonb_typeof(material)='object'),
 facts JSONB NOT NULL CHECK(jsonb_typeof(facts)='object'),
 resolved_units JSONB NOT NULL CHECK(jsonb_typeof(resolved_units)='array' AND jsonb_array_length(resolved_units)>=2),
 status TEXT NOT NULL DEFAULT 'reviewed_not_executable' CHECK(status='reviewed_not_executable'),
 reason TEXT NOT NULL CHECK(length(reason) BETWEEN 1 AND 240),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER tv_coverage_candidate_review_immutable BEFORE UPDATE OR DELETE ON tv_coverage_candidate_review FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM tv_coverage_candidate_review) THEN RAISE EXCEPTION 'TV coverage review history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE tv_coverage_candidate_review;
