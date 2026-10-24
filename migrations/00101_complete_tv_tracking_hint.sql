-- +goose Up
CREATE TABLE complete_tv_tracking_hint (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 task_id UUID NOT NULL REFERENCES download_task(id),
 operation_id UUID NOT NULL REFERENCES complete_tv_execution_authority(operation_id),
 prior_job_id UUID NOT NULL REFERENCES job(id),
 hint_id UUID NOT NULL,
 instance_id UUID NOT NULL,
 sequence BIGINT NOT NULL CHECK(sequence>0),
 kind TEXT NOT NULL CHECK(kind IN ('close_write','moved_to')),
 relative_path TEXT NOT NULL CHECK(length(relative_path) BETWEEN 1 AND 4096),
 binding JSONB NOT NULL CHECK(jsonb_typeof(binding)='object'),
 source_device BIGINT NOT NULL,
 source_inode BIGINT NOT NULL CHECK(source_inode<>0),
 prior_digest TEXT NOT NULL CHECK(prior_digest ~ '^[0-9a-f]{64}$'),
 authority_digest TEXT NOT NULL CHECK(authority_digest ~ '^[0-9a-f]{64}$'),
 preparation_digest TEXT NOT NULL CHECK(preparation_digest ~ '^[0-9a-f]{64}$'),
 protection_digest TEXT NOT NULL CHECK(protection_digest ~ '^[0-9a-f]{64}$'),
 request_digest TEXT NOT NULL CHECK(request_digest ~ '^[0-9a-f]{64}$'),
 accepted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(operation_id,hint_id),
 UNIQUE(operation_id,prior_job_id),
 CHECK(job_id<>prior_job_id)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_complete_tv_tracking_hint_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'complete TV tracking hint history is immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER complete_tv_tracking_hint_immutable BEFORE UPDATE OR DELETE ON complete_tv_tracking_hint FOR EACH ROW EXECUTE FUNCTION pulse_complete_tv_tracking_hint_history();
CREATE TRIGGER complete_tv_tracking_hint_no_truncate BEFORE TRUNCATE ON complete_tv_tracking_hint FOR EACH STATEMENT EXECUTE FUNCTION pulse_complete_tv_tracking_hint_history();
-- +goose Down
LOCK TABLE complete_tv_tracking_hint IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM complete_tv_tracking_hint) THEN RAISE EXCEPTION 'complete TV tracking hint history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE complete_tv_tracking_hint;
DROP FUNCTION pulse_complete_tv_tracking_hint_history();
