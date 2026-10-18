-- +goose Up
CREATE TABLE movie_upgrade_send_dispatch (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_send_authorization(operation_id) ON DELETE RESTRICT,
 dispatch_id UUID NOT NULL UNIQUE CHECK(dispatch_id<>'00000000-0000-0000-0000-000000000000'::uuid),
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 owner UUID NOT NULL,
 token UUID NOT NULL UNIQUE,
 session_nonce UUID NOT NULL,
 advisory_key BIGINT NOT NULL,
 backend_pid INTEGER NOT NULL,
 backend_start TIMESTAMPTZ NOT NULL,
 arm_sha256 BYTEA NOT NULL CHECK(octet_length(arm_sha256)=32),
 raw_sha256 BYTEA NOT NULL CHECK(octet_length(raw_sha256)=32),
 arm_lease_until TIMESTAMPTZ NOT NULL,
 dispatched_at TIMESTAMPTZ NOT NULL,
 CHECK(dispatched_at<arm_lease_until)
);

-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_send_dispatch_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'upgrade dispatch history is immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER movie_upgrade_send_dispatch_immutable BEFORE UPDATE OR DELETE OR TRUNCATE
 ON movie_upgrade_send_dispatch FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_send_dispatch_immutable();

-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_send_dispatch_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a movie_upgrade_send_authorization%ROWTYPE;
DECLARE p movie_upgrade_preparation%ROWTYPE;
DECLARE task JSONB;
BEGIN
 SELECT * INTO a FROM movie_upgrade_send_authorization WHERE operation_id=NEW.operation_id FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'upgrade dispatch authorization missing'; END IF;
 SELECT * INTO p FROM movie_upgrade_preparation WHERE operation_id=NEW.operation_id FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'upgrade dispatch preparation missing'; END IF;
 SELECT to_jsonb(t) INTO task FROM download_task t WHERE id=a.task_id FOR SHARE;
 IF NOT FOUND OR a.task_snapshot IS DISTINCT FROM task OR a.preparation_snapshot IS DISTINCT FROM to_jsonb(p)
  OR p.evidence->'proof' IS DISTINCT FROM a.proof OR p.task_snapshot IS DISTINCT FROM task
  OR task->>'state' IS DISTINCT FROM 'submitting' OR a.status IS DISTINCT FROM 'armed_not_sent'
  OR NEW.task_id IS DISTINCT FROM a.task_id OR NEW.actor_id IS DISTINCT FROM a.actor_id
  OR NEW.owner IS DISTINCT FROM a.owner OR NEW.token IS DISTINCT FROM a.token
  OR NEW.session_nonce IS DISTINCT FROM a.session_nonce OR NEW.advisory_key IS DISTINCT FROM a.advisory_key
  OR NEW.backend_pid IS DISTINCT FROM a.backend_pid OR NEW.backend_start IS DISTINCT FROM a.backend_start
  OR NEW.arm_lease_until IS DISTINCT FROM a.lease_until
  OR NEW.arm_sha256 IS DISTINCT FROM sha256(convert_to(to_jsonb(a)::text,'UTF8'))
  OR NEW.raw_sha256 IS DISTINCT FROM p.raw_sha256
  OR EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=NEW.operation_id) THEN
  RAISE EXCEPTION 'upgrade dispatch binding mismatch';
 END IF;
 IF a.lease_until<=clock_timestamp() THEN RAISE EXCEPTION 'upgrade dispatch lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_dispatch_lease_valid'; END IF;
 IF a.backend_pid IS DISTINCT FROM pg_backend_pid()
  OR a.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
  OR a.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_send_nonce',true)
  OR a.advisory_key IS DISTINCT FROM (('x'||substr(encode(sha256(convert_to('pulsemedia.movie-upgrade.physical.v1:'||NEW.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint)
  OR NOT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory'
    AND mode='ExclusiveLock' AND granted AND objsubid=1
    AND classid=((a.advisory_key>>32)&4294967295)::oid AND objid=(a.advisory_key&4294967295)::oid)
  OR a.lease_until<=clock_timestamp() OR NEW.dispatched_at>clock_timestamp() THEN
  RAISE EXCEPTION 'upgrade dispatch session or lease invalid';
 END IF;
 PERFORM pg_advisory_xact_lock_shared(a.advisory_key);
 IF NOT pg_advisory_unlock(a.advisory_key) THEN RAISE EXCEPTION 'original session lock required'; END IF;
 IF NOT pg_try_advisory_lock(a.advisory_key) THEN RAISE EXCEPTION 'original session lock could not be restored'; END IF;
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 IF a.lease_until<=clock_timestamp() THEN RAISE EXCEPTION 'upgrade dispatch lease expired after graph checks' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_dispatch_lease_valid'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_send_dispatch_graph AFTER INSERT ON movie_upgrade_send_dispatch
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_dispatch_guard();

-- +goose Down
LOCK TABLE movie_upgrade_send_dispatch,movie_upgrade_send_result,movie_upgrade_send_authorization,
 movie_upgrade_preparation,movie_upgrade_preparation_lease,movie_upgrade_authority,
 download_task,acquisition_reservation,unit_reservation,download_requirement,job,audit_log
 IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.dispatch') THEN
  RAISE EXCEPTION 'upgrade dispatch history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_send_dispatch;
DROP FUNCTION pulse_upgrade_send_dispatch_guard();
DROP FUNCTION pulse_upgrade_send_dispatch_immutable();
