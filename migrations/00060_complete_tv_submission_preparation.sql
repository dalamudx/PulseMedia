-- +goose Up
CREATE TABLE complete_tv_submission_preparation (
 operation_id UUID PRIMARY KEY REFERENCES complete_tv_execution_authority(operation_id),
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id),
 job_id UUID NOT NULL REFERENCES job(id),
 lease_owner TEXT NOT NULL,
 lease_token BIGINT NOT NULL,
 info_hash TEXT NOT NULL CHECK(info_hash ~ '^[0-9a-f]{40}$'),
 torrent_sha256 BYTEA NOT NULL CHECK(octet_length(torrent_sha256)=32),
 proof JSONB NOT NULL CHECK(jsonb_typeof(proof)='object' AND proof->>'kind'='complete_tv_prepared_v1'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE complete_tv_submission_unit (
 task_id UUID NOT NULL REFERENCES complete_tv_submission_preparation(task_id),
 unit_id UUID NOT NULL REFERENCES media_unit(id),
 unit_version BIGINT NOT NULL CHECK(unit_version>0),
 provider_episode_id BIGINT NOT NULL CHECK(provider_episode_id>0),
 unit_key TEXT NOT NULL,
 video_path TEXT NOT NULL,
 video_size BIGINT NOT NULL CHECK(video_size>0),
 PRIMARY KEY(task_id,unit_id),
 UNIQUE(task_id,video_path),
 UNIQUE(task_id,provider_episode_id)
);
CREATE TRIGGER complete_tv_preparation_immutable BEFORE UPDATE OR DELETE ON complete_tv_submission_preparation FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER complete_tv_prepared_unit_immutable BEFORE UPDATE OR DELETE ON complete_tv_submission_unit FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM complete_tv_submission_preparation) THEN RAISE EXCEPTION 'complete TV preparation history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE complete_tv_submission_unit;
DROP TABLE complete_tv_submission_preparation;

