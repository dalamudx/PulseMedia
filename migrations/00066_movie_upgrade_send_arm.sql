-- +goose Up
CREATE TABLE movie_upgrade_send_authorization (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_preparation(operation_id) ON DELETE RESTRICT,
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 owner UUID NOT NULL CHECK(owner <> '00000000-0000-0000-0000-000000000000'::uuid),
 token UUID NOT NULL UNIQUE CHECK(token <> '00000000-0000-0000-0000-000000000000'::uuid),
 session_nonce UUID NOT NULL UNIQUE CHECK(session_nonce <> '00000000-0000-0000-0000-000000000000'::uuid),
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 backend_start TIMESTAMPTZ NOT NULL,
 advisory_key BIGINT NOT NULL,
 armed_at TIMESTAMPTZ NOT NULL,
 lease_until TIMESTAMPTZ NOT NULL CHECK(lease_until=armed_at+interval '60 seconds'),
 status TEXT NOT NULL CHECK(status='armed_not_sent'),
 preparation_snapshot JSONB NOT NULL CHECK(jsonb_typeof(preparation_snapshot)='object'),
 task_snapshot JSONB NOT NULL CHECK(jsonb_typeof(task_snapshot)='object'),
 proof JSONB NOT NULL CHECK(jsonb_typeof(proof)='object')
);
CREATE TRIGGER movie_upgrade_send_authorization_immutable BEFORE UPDATE OR DELETE ON movie_upgrade_send_authorization
 FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_send_arm_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p movie_upgrade_preparation%ROWTYPE;
BEGIN
 SELECT * INTO p FROM movie_upgrade_preparation WHERE operation_id=NEW.operation_id;
 IF NOT FOUND OR p.task_id<>NEW.task_id OR p.actor_id<>NEW.actor_id
  OR to_jsonb(p) IS DISTINCT FROM NEW.preparation_snapshot
  OR p.task_snapshot IS DISTINCT FROM NEW.task_snapshot
  OR p.evidence->'proof' IS DISTINCT FROM NEW.proof THEN
  RAISE EXCEPTION 'upgrade send binding mismatch';
 END IF;
 IF NEW.backend_pid<>pg_backend_pid()
  OR NEW.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
  OR NEW.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_send_nonce',true)
  OR NEW.advisory_key IS DISTINCT FROM (('x'||substr(encode(sha256(convert_to('pulsemedia.movie-upgrade.physical.v1:'||NEW.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint)
  OR NOT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory'
   AND mode='ExclusiveLock' AND granted AND objsubid=1 AND classid=((NEW.advisory_key>>32)&4294967295)::oid
   AND objid=(NEW.advisory_key&4294967295)::oid)
  OR NEW.armed_at>clock_timestamp() OR NEW.lease_until<=clock_timestamp() THEN
  RAISE EXCEPTION 'upgrade send session or lease invalid';
 END IF;
 -- pg_locks alone cannot distinguish session and transaction ownership.
 -- Hold a shared transaction lock across the session unlock/relock so a
 -- canonical exclusive executor cannot enter during this proof step.
 PERFORM pg_advisory_xact_lock_shared(NEW.advisory_key);
 IF NOT pg_advisory_unlock(NEW.advisory_key) THEN
  RAISE EXCEPTION 'original exclusive session lock is required';
 END IF;
 IF NOT pg_try_advisory_lock(NEW.advisory_key) THEN
  RAISE EXCEPTION 'original session lock could not be restored';
 END IF;
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_send_arm_graph AFTER INSERT ON movie_upgrade_send_authorization
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_arm_guard();

-- +goose Down
LOCK TABLE movie_upgrade_send_authorization,movie_upgrade_preparation,movie_upgrade_preparation_lease,
 movie_upgrade_authority,download_task,acquisition_reservation,unit_reservation,download_requirement,job,audit_log
 IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_send_authorization)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.arm') THEN
  RAISE EXCEPTION 'upgrade send history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_send_authorization;
DROP FUNCTION pulse_upgrade_send_arm_guard();
