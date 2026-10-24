-- +goose Up
-- New sends preserve the actual client configuration; historical tasks stay unbound.
CREATE TABLE download_task_client_binding (
 task_id UUID PRIMARY KEY REFERENCES download_task(id),
 client_id UUID NOT NULL REFERENCES download_client(id),
 client_stamp TEXT NOT NULL CHECK(client_stamp COLLATE "C" ~ '^[0-9a-f]{64}$'),
 job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(length(lease_owner) BETWEEN 1 AND 160),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 prepared_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION pulse_download_task_binding_history() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'download client binding history protected' USING ERRCODE='23514';
END;
$$;
CREATE FUNCTION pulse_assert_download_task_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE t download_task%ROWTYPE; j job%ROWTYPE; stamp TEXT;
BEGIN
 SELECT * INTO STRICT t FROM download_task WHERE id=NEW.task_id FOR SHARE;
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR SHARE;
 PERFORM 1 FROM download_client WHERE id=NEW.client_id FOR SHARE;
 SELECT pulse_global_rates_binding(NEW.client_id) INTO stamp;
 IF t.client_id<>NEW.client_id OR t.state<>'submitting'
    OR stamp IS DISTINCT FROM NEW.client_stamp
    OR j.kind NOT IN ('download_submission','episode_download_submission')
    OR j.state<>'running' OR j.lease_owner IS DISTINCT FROM NEW.lease_owner
    OR j.lease_token<>NEW.lease_token OR j.lease_until IS NULL
    OR j.lease_until<=clock_timestamp()
    OR j.payload->>'operation_id' IS DISTINCT FROM t.operation_id::text
    OR j.payload->>'candidate_id' IS DISTINCT FROM t.candidate_id::text THEN
  RAISE EXCEPTION 'download client binding rejected' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER download_task_client_binding_history
 BEFORE UPDATE OR DELETE ON download_task_client_binding FOR EACH ROW
 EXECUTE FUNCTION pulse_download_task_binding_history();
CREATE TRIGGER download_task_client_binding_truncate
 BEFORE TRUNCATE ON download_task_client_binding FOR EACH STATEMENT
 EXECUTE FUNCTION pulse_download_task_binding_history();
CREATE CONSTRAINT TRIGGER download_task_client_binding_assert
 AFTER INSERT ON download_task_client_binding DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION pulse_assert_download_task_binding();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM download_task_client_binding) THEN
  RAISE EXCEPTION 'download client binding history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE download_task_client_binding;
DROP FUNCTION pulse_assert_download_task_binding();
DROP FUNCTION pulse_download_task_binding_history();
