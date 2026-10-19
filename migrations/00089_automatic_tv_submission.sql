-- +goose Up
CREATE TABLE automatic_tv_physical_attempt (
 id UUID PRIMARY KEY,
 operation_id UUID NOT NULL REFERENCES automatic_tv_execution_authority(operation_id),
 job_id UUID NOT NULL REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 advisory_key BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX automatic_tv_physical_attempt_active ON automatic_tv_physical_attempt(operation_id) WHERE finished_at IS NULL;
CREATE TABLE automatic_tv_submission_preparation (
 operation_id UUID PRIMARY KEY REFERENCES automatic_tv_execution_authority(operation_id),
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id),
 physical_attempt_id UUID NOT NULL UNIQUE REFERENCES automatic_tv_physical_attempt(id),
 job_id UUID NOT NULL REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 authority_hash TEXT NOT NULL CHECK(authority_hash ~ '^[0-9a-f]{64}$'),
 info_hash TEXT NOT NULL CHECK(info_hash ~ '^[0-9a-f]{40}$'),
 torrent_sha256 BYTEA NOT NULL CHECK(octet_length(torrent_sha256)=32),
 proof_hash TEXT NOT NULL CHECK(proof_hash ~ '^[0-9a-f]{64}$'),
 proof JSONB NOT NULL CHECK(COALESCE(jsonb_typeof(proof)='object' AND proof->>'kind'='automatic_tv_prepared_v1',false)),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE automatic_tv_submission_result (
 operation_id UUID PRIMARY KEY REFERENCES automatic_tv_submission_preparation(operation_id),
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id),
 physical_attempt_id UUID NOT NULL UNIQUE REFERENCES automatic_tv_physical_attempt(id),
 job_id UUID NOT NULL REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 state TEXT NOT NULL CHECK(state IN ('tracking','submission_unknown')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_submission_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'automatic TV submission history is immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_preparation_history BEFORE UPDATE OR DELETE ON automatic_tv_submission_preparation FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_preparation_truncate BEFORE TRUNCATE ON automatic_tv_submission_preparation FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_result_history BEFORE UPDATE OR DELETE ON automatic_tv_submission_result FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_result_truncate BEFORE TRUNCATE ON automatic_tv_submission_result FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();
-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_physical_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.finished_at IS NULL AND NEW.finished_at IS NOT NULL
  AND NEW.finished_at>=OLD.created_at
  AND (to_jsonb(NEW)-'finished_at')=(to_jsonb(OLD)-'finished_at') THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'automatic TV physical attempt history is immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_physical_history BEFORE UPDATE OR DELETE ON automatic_tv_physical_attempt FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_physical_attempt();
CREATE TRIGGER automatic_tv_physical_truncate BEFORE TRUNCATE ON automatic_tv_physical_attempt FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_physical_attempt();
-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_submission_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p automatic_tv_submission_preparation; a automatic_tv_execution_authority; x automatic_tv_physical_attempt;
BEGIN
 IF TG_TABLE_NAME='automatic_tv_submission_preparation' THEN p:=NEW;
 ELSE SELECT * INTO p FROM automatic_tv_submission_preparation WHERE operation_id=NEW.operation_id;
  IF p.task_id IS DISTINCT FROM NEW.task_id OR p.physical_attempt_id IS DISTINCT FROM NEW.physical_attempt_id
   OR p.job_id IS DISTINCT FROM NEW.job_id OR p.lease_owner IS DISTINCT FROM NEW.lease_owner OR p.lease_token IS DISTINCT FROM NEW.lease_token
  THEN RAISE EXCEPTION 'automatic TV result requires original preparation'; END IF;
 END IF;
 SELECT * INTO a FROM automatic_tv_execution_authority WHERE operation_id=p.operation_id;
 SELECT * INTO x FROM automatic_tv_physical_attempt WHERE id=p.physical_attempt_id;
 IF a.operation_id IS NULL OR x.id IS NULL OR x.finished_at IS NOT NULL
  OR x.operation_id<>p.operation_id OR x.job_id<>p.job_id OR x.lease_owner<>p.lease_owner OR x.lease_token<>p.lease_token
  OR p.authority_hash<>a.decision_stamp
  OR NOT EXISTS(SELECT 1 FROM download_task t WHERE t.id=p.task_id AND t.operation_id=p.operation_id AND t.candidate_id=a.candidate_id AND t.client_id=a.client_id AND t.info_hash=p.info_hash)
 THEN RAISE EXCEPTION 'automatic TV preparation requires original physical binding'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_preparation_binding BEFORE INSERT ON automatic_tv_submission_preparation FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_submission_binding();
CREATE TRIGGER automatic_tv_result_binding BEFORE INSERT ON automatic_tv_submission_result FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_submission_binding();
-- +goose Down
LOCK TABLE automatic_tv_physical_attempt,automatic_tv_submission_preparation,automatic_tv_submission_result IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_physical_attempt) OR EXISTS(SELECT 1 FROM automatic_tv_submission_preparation) OR EXISTS(SELECT 1 FROM automatic_tv_submission_result)
 THEN RAISE EXCEPTION 'automatic TV submission history downgrade refused'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE automatic_tv_submission_result,automatic_tv_submission_preparation,automatic_tv_physical_attempt;
DROP FUNCTION check_automatic_tv_submission_binding();
DROP FUNCTION guard_automatic_tv_submission_history();
DROP FUNCTION guard_automatic_tv_physical_attempt();
