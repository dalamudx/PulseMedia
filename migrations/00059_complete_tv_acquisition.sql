-- +goose Up
CREATE TABLE complete_tv_execution_authority (
 operation_id UUID PRIMARY KEY,
 review_id UUID NOT NULL REFERENCES tv_coverage_candidate_review(id),
 subscription_id UUID NOT NULL REFERENCES subscription(id),
 candidate_id UUID NOT NULL REFERENCES release_candidate(id),
 root_id UUID NOT NULL REFERENCES library_root(id),
 client_id UUID NOT NULL REFERENCES download_client(id),
 actor_id UUID NOT NULL REFERENCES app_user(id),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND snapshot->>'kind'='reviewed_complete_tv_coverage_v1' AND jsonb_typeof(snapshot->'units')='array' AND jsonb_array_length(snapshot->'units')>=2),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER complete_tv_execution_authority_immutable BEFORE UPDATE OR DELETE ON complete_tv_execution_authority FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM complete_tv_execution_authority) THEN RAISE EXCEPTION 'complete TV authority history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE complete_tv_execution_authority;

