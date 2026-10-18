-- +goose Up
-- No timeout: losing a session does not prove physical I/O has returned.
CREATE TABLE complete_tv_physical_permit (
 operation_id UUID PRIMARY KEY REFERENCES complete_tv_execution_authority(operation_id),
 token UUID NOT NULL UNIQUE,
 backend_pid INTEGER NOT NULL CHECK(backend_pid > 0),
 advisory_key BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- +goose Down
-- Hold through BOTH the emptiness check and DROP (Goose transaction).
-- An already-admitted inserter must finish before we inspect retained permits.
LOCK TABLE complete_tv_physical_permit IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM complete_tv_physical_permit) THEN
  RAISE EXCEPTION 'cannot discard retained complete TV physical execution permits';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE complete_tv_physical_permit;
