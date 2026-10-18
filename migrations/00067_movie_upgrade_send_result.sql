-- +goose Up
CREATE TABLE movie_upgrade_send_result (
 operation_id UUID PRIMARY KEY
  REFERENCES movie_upgrade_send_authorization(operation_id) ON DELETE RESTRICT,
 task_id UUID NOT NULL UNIQUE
  REFERENCES download_task(id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL
  REFERENCES app_user(id) ON DELETE RESTRICT,
 owner UUID NOT NULL
  CHECK(owner <> '00000000-0000-0000-0000-000000000000'::uuid),
 token UUID NOT NULL UNIQUE
  CHECK(token <> '00000000-0000-0000-0000-000000000000'::uuid),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 130),
 status TEXT NOT NULL CHECK(status IN ('confirmed','unknown')),
 returned_at TIMESTAMPTZ NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL,
 raw_sha256 BYTEA NOT NULL CHECK(octet_length(raw_sha256)=32),
 arm_sha256 BYTEA NOT NULL CHECK(octet_length(arm_sha256)=32),
 arm_snapshot JSONB NOT NULL
  CHECK(jsonb_typeof(arm_snapshot)='object'),
 CHECK(returned_at <= recorded_at)
);

CREATE TRIGGER movie_upgrade_send_result_immutable
 BEFORE UPDATE OR DELETE ON movie_upgrade_send_result
 FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();

-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_send_result_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 a movie_upgrade_send_authorization%ROWTYPE;
 p movie_upgrade_preparation%ROWTYPE;
 task_json JSONB;
 now_at TIMESTAMPTZ;
 expected_key BIGINT;
BEGIN
 SELECT * INTO a
 FROM movie_upgrade_send_authorization
 WHERE operation_id=NEW.operation_id
 FOR SHARE;

 IF NOT FOUND THEN
  RAISE EXCEPTION 'upgrade send authorization missing';
 END IF;

 SELECT * INTO p
 FROM movie_upgrade_preparation
 WHERE operation_id=NEW.operation_id
 FOR SHARE;

 IF NOT FOUND THEN
  RAISE EXCEPTION 'upgrade send preparation missing';
 END IF;

 SELECT to_jsonb(t) INTO task_json
 FROM download_task t
 WHERE t.id=a.task_id
 FOR SHARE;

 IF NOT FOUND THEN
  RAISE EXCEPTION 'upgrade send task missing';
 END IF;

 expected_key :=
  (('x'||substr(
   encode(sha256(convert_to(
    'pulsemedia.movie-upgrade.physical.v1:'||NEW.operation_id::text,
    'UTF8'
   )),'hex'),1,16
  ))::bit(64)::bigint);

 IF NEW.arm_snapshot IS DISTINCT FROM to_jsonb(a)
  OR NEW.arm_sha256 IS DISTINCT FROM
   sha256(convert_to(to_jsonb(a)::text,'UTF8'))
  OR NEW.raw_sha256 IS DISTINCT FROM p.raw_sha256
  OR NEW.task_id IS DISTINCT FROM a.task_id
  OR NEW.actor_id IS DISTINCT FROM a.actor_id
  OR NEW.owner IS DISTINCT FROM a.owner
  OR NEW.token IS DISTINCT FROM a.token
  OR a.status IS DISTINCT FROM 'armed_not_sent'
  OR a.preparation_snapshot IS DISTINCT FROM to_jsonb(p)
  OR a.task_snapshot IS DISTINCT FROM task_json
  OR p.task_snapshot IS DISTINCT FROM task_json
  OR p.task_id IS DISTINCT FROM a.task_id
  OR p.actor_id IS DISTINCT FROM a.actor_id
  OR p.evidence->'proof' IS DISTINCT FROM a.proof
  OR a.advisory_key IS DISTINCT FROM expected_key THEN
  RAISE EXCEPTION 'upgrade send result binding mismatch';
 END IF;

 IF a.backend_pid IS DISTINCT FROM pg_backend_pid()
  OR a.backend_start IS DISTINCT FROM
   (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
  OR a.session_nonce::text IS DISTINCT FROM
   current_setting('pulsemedia.upgrade_send_nonce',true)
  OR NOT EXISTS(
   SELECT 1 FROM pg_locks
   WHERE pid=pg_backend_pid()
    AND locktype='advisory'
    AND mode='ExclusiveLock'
    AND granted
    AND objsubid=1
    AND classid=((a.advisory_key>>32)&4294967295)::oid
    AND objid=(a.advisory_key&4294967295)::oid
  ) THEN
  RAISE EXCEPTION 'upgrade send result session invalid';
 END IF;

 -- Prove session ownership, not merely transaction ownership.
 -- Keep this shared transaction lock until transaction end.
 PERFORM pg_advisory_xact_lock_shared(a.advisory_key);

 IF NOT pg_advisory_unlock(a.advisory_key) THEN
  RAISE EXCEPTION 'original exclusive session lock is required';
 END IF;

 IF NOT pg_try_advisory_lock(a.advisory_key) THEN
  RAISE EXCEPTION 'original session lock could not be restored';
 END IF;

 -- Capture database time only after the row/session lock checks.
 now_at := clock_timestamp();

 IF a.owner IS DISTINCT FROM NEW.owner
  OR a.token IS DISTINCT FROM NEW.token
  OR a.armed_at > now_at
  OR a.lease_until <= now_at
  OR NEW.returned_at < a.armed_at
  OR NEW.returned_at > NEW.recorded_at
  OR NEW.recorded_at > now_at
  OR NEW.recorded_at >= a.lease_until THEN
  RAISE EXCEPTION 'upgrade send result lease invalid';
 END IF;

 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER movie_upgrade_send_result_graph
 AFTER INSERT ON movie_upgrade_send_result
 DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_result_guard();

-- +goose Down
LOCK TABLE movie_upgrade_send_result,movie_upgrade_send_authorization,
 movie_upgrade_preparation,download_task,audit_log
 IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_send_result)
 OR EXISTS(SELECT 1 FROM movie_upgrade_send_authorization)
 OR EXISTS(
  SELECT 1 FROM audit_log
  WHERE action='upgrade.send-result'
 ) THEN
  RAISE EXCEPTION 'upgrade send result history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd

DROP TABLE movie_upgrade_send_result;
DROP FUNCTION pulse_upgrade_send_result_guard();
