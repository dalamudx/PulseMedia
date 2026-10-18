-- +goose Up
CREATE TABLE download_file_inventory (
 task_id UUID PRIMARY KEY REFERENCES download_task(id), operation_id UUID NOT NULL REFERENCES episode_execution_authority(operation_id),
 client_id UUID NOT NULL REFERENCES download_client(id), info_hash TEXT NOT NULL CHECK(info_hash ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
 total_bytes BIGINT NOT NULL CHECK(total_bytes>0), content_path TEXT NOT NULL, client_stamp TEXT NOT NULL CHECK(client_stamp ~ '^[0-9a-f]{64}$'),
 files JSONB NOT NULL CHECK(jsonb_typeof(files)='array' AND jsonb_array_length(files) BETWEEN 1 AND 1000),
 observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION pulse_download_file_inventory_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'download file inventory evidence is immutable';END$$;
-- +goose StatementEnd
CREATE TRIGGER download_file_inventory_guard BEFORE UPDATE OR DELETE ON download_file_inventory FOR EACH ROW EXECUTE FUNCTION pulse_download_file_inventory_guard();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM download_file_inventory) THEN RAISE EXCEPTION 'download inventory history cannot be discarded';END IF;END$$;
-- +goose StatementEnd
DROP TABLE download_file_inventory;
DROP FUNCTION pulse_download_file_inventory_guard();
