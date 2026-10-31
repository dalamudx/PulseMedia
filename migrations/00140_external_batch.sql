-- +goose Up
CREATE TABLE external_import_batch (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES app_user(id),
 scope_id UUID NOT NULL REFERENCES untracked_directory_scope(id),
 command_key TEXT NOT NULL CHECK(length(command_key) BETWEEN 1 AND 160),
 input_hash TEXT NOT NULL CHECK(length(input_hash)=64),
 manifest JSONB NOT NULL CHECK(jsonb_typeof(manifest)='object' AND jsonb_array_length(manifest->'rows') BETWEEN 1 AND 20 AND manifest->>'execution_allowed'='false'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(owner_id,scope_id,command_key)
);
CREATE TRIGGER external_import_batch_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON external_import_batch FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_review_history_guard();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM external_import_batch) THEN RAISE EXCEPTION 'external batch history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE external_import_batch;
