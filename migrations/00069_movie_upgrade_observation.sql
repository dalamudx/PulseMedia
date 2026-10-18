-- +goose Up
CREATE TABLE movie_upgrade_observation (
 observation_id UUID PRIMARY KEY CHECK(observation_id<>'00000000-0000-0000-0000-000000000000'::uuid),
 operation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_send_result(operation_id) ON DELETE RESTRICT,
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 owner UUID NOT NULL CHECK(owner<>'00000000-0000-0000-0000-000000000000'::uuid),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 130),
 token UUID NOT NULL UNIQUE CHECK(token<>'00000000-0000-0000-0000-000000000000'::uuid),
 session_nonce UUID NOT NULL UNIQUE,
 backend_pid INTEGER NOT NULL,
 backend_start TIMESTAMPTZ NOT NULL,
 advisory_key BIGINT NOT NULL,
 admitted_at TIMESTAMPTZ NOT NULL,
 lease_until TIMESTAMPTZ NOT NULL CHECK(lease_until=admitted_at+interval '60 seconds'),
 client_id UUID NOT NULL REFERENCES download_client(id) ON DELETE RESTRICT,
 infohash TEXT NOT NULL CHECK(infohash~'^[0-9a-f]{40}$'),
 save_path TEXT NOT NULL CHECK(length(save_path)>0),
 task_tag TEXT NOT NULL CHECK(length(task_tag)>0),
 total_bytes BIGINT NOT NULL CHECK(total_bytes>0),
 original_sha256 BYTEA NOT NULL CHECK(octet_length(original_sha256)=32),
 protected_sha256 BYTEA NOT NULL CHECK(octet_length(protected_sha256)=32),
 UNIQUE(observation_id,operation_id)
);
CREATE TABLE movie_upgrade_observation_permit (
 observation_id UUID PRIMARY KEY,
 operation_id UUID NOT NULL UNIQUE,
 issued_at TIMESTAMPTZ NOT NULL,
 FOREIGN KEY(observation_id,operation_id) REFERENCES movie_upgrade_observation(observation_id,operation_id) ON DELETE RESTRICT
);
CREATE TABLE movie_upgrade_observation_result (
 observation_id UUID PRIMARY KEY REFERENCES movie_upgrade_observation_permit(observation_id) ON DELETE RESTRICT,
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 130),
 outcome TEXT NOT NULL CHECK(outcome IN ('exact_match','absent','ambiguous','unknown')),
 returned_at TIMESTAMPTZ NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL,
 observation_sha256 BYTEA NOT NULL CHECK(octet_length(observation_sha256)=32),
 permit_sha256 BYTEA NOT NULL CHECK(octet_length(permit_sha256)=32),
 disposition TEXT NOT NULL CHECK(disposition='manual_review'),
 CHECK(returned_at<=recorded_at)
);

-- The digest is NULL unless the complete original UNKNOWN-return graph agrees.
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_observation_original_digest(op UUID) RETURNS BYTEA LANGUAGE SQL STABLE AS $$
 SELECT sha256(convert_to(jsonb_build_array(to_jsonb(f),to_jsonb(p),to_jsonb(a),to_jsonb(d),to_jsonb(r),to_jsonb(t))::text,'UTF8'))
 FROM movie_upgrade_authority f
 JOIN movie_upgrade_preparation p USING(operation_id)
 JOIN movie_upgrade_send_authorization a USING(operation_id)
 JOIN movie_upgrade_send_dispatch d USING(operation_id)
 JOIN movie_upgrade_send_result r USING(operation_id)
 JOIN download_task t ON t.id=p.task_id
 WHERE f.operation_id=op AND r.status='unknown'
 AND a.preparation_snapshot=to_jsonb(p) AND a.task_snapshot=to_jsonb(t)
 AND p.task_snapshot=to_jsonb(t) AND p.evidence->'proof'=a.proof
 AND r.arm_snapshot=to_jsonb(a)
 AND r.arm_sha256=sha256(convert_to(to_jsonb(a)::text,'UTF8'))
 AND d.arm_sha256=r.arm_sha256 AND d.raw_sha256=p.raw_sha256 AND r.raw_sha256=p.raw_sha256
 AND a.task_id=t.id AND d.task_id=t.id AND r.task_id=t.id
 AND a.actor_id=p.actor_id AND a.actor_id=r.actor_id AND a.actor_id=d.actor_id
 AND r.owner=a.owner AND r.token=a.token AND d.owner=a.owner AND d.token=a.token
$$;
CREATE FUNCTION pulse_upgrade_observation_protected_digest(op UUID) RETURNS BYTEA LANGUAGE SQL STABLE AS $$
 SELECT sha256(convert_to(jsonb_build_object(
 'root',(SELECT to_jsonb(x) FROM library_root x WHERE id=a.root_id),
 'client',(SELECT to_jsonb(x) FROM download_client x WHERE id=a.client_id),
 'subscription',(SELECT to_jsonb(x) FROM subscription x WHERE id=a.subscription_id),
 'unit',(SELECT to_jsonb(x) FROM media_unit x WHERE id=a.unit_id),
 'file',(SELECT to_jsonb(x) FROM library_file x WHERE id=a.old_file_id),
 'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE x.source_file_id=a.old_file_id OR x.unit_id=a.unit_id),
 'mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.file_id,x.unit_id) FROM library_file_unit x WHERE x.file_id=a.old_file_id OR x.unit_id=a.unit_id),
 'lanes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=a.operation_id),
 'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=a.operation_id),
 'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=a.operation_id)
 )::text,'UTF8')) FROM movie_upgrade_authority a WHERE a.operation_id=op
$$;
CREATE FUNCTION pulse_assert_upgrade_observation(obs UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE o movie_upgrade_observation%ROWTYPE; ok BOOLEAN; now_at TIMESTAMPTZ;
BEGIN
 SELECT * INTO o FROM movie_upgrade_observation WHERE observation_id=obs FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'upgrade observation missing'; END IF;
 PERFORM pulse_assert_upgrade_preparation(o.operation_id);
 IF o.original_sha256 IS DISTINCT FROM pulse_upgrade_observation_original_digest(o.operation_id)
 OR o.protected_sha256 IS DISTINCT FROM pulse_upgrade_observation_protected_digest(o.operation_id)
 OR o.advisory_key IS DISTINCT FROM (('x'||substr(encode(sha256(convert_to('pulsemedia.movie-upgrade.physical.v1:'||o.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint) THEN
  RAISE EXCEPTION 'upgrade observation graph mismatch';
 END IF;
 SELECT EXISTS(SELECT 1 FROM movie_upgrade_preparation p JOIN movie_upgrade_authority a USING(operation_id)
 WHERE p.operation_id=o.operation_id AND p.actor_id=o.actor_id AND a.actor_id=o.actor_id
 AND p.task_id=o.task_id AND a.client_id=o.client_id
 AND o.infohash=p.evidence->'proof'->>'info_hash'
 AND o.total_bytes=(p.evidence->'proof'->>'total_bytes')::bigint
 AND o.save_path=p.evidence->>'save_path'
 AND o.task_tag='pulsemedia:op:'||o.operation_id::text) INTO ok;
 IF NOT ok OR NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_permit x WHERE x.observation_id=obs AND x.operation_id=o.operation_id AND x.issued_at=o.admitted_at) THEN
  RAISE EXCEPTION 'upgrade observation bindings mismatch';
 END IF;
 IF o.backend_pid IS DISTINCT FROM pg_backend_pid()
 OR o.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 OR o.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_observation_nonce',true)
 OR NOT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock' AND objsubid=1 AND classid=((o.advisory_key>>32)&4294967295)::oid AND objid=(o.advisory_key&4294967295)::oid) THEN
  RAISE EXCEPTION 'upgrade observation original session required';
 END IF;
 PERFORM pg_advisory_xact_lock_shared(o.advisory_key);
 IF NOT pg_advisory_unlock(o.advisory_key) OR NOT pg_try_advisory_lock(o.advisory_key) THEN
  RAISE EXCEPTION 'upgrade observation session lock required';
 END IF;
 now_at:=clock_timestamp();
 IF o.admitted_at>now_at OR o.lease_until<=now_at THEN
  RAISE EXCEPTION 'upgrade observation lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_observation_lease';
 END IF;
END $$;
CREATE FUNCTION pulse_upgrade_observation_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pulse_assert_upgrade_observation(NEW.observation_id);
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_observation_result_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE o movie_upgrade_observation%ROWTYPE; p movie_upgrade_observation_permit%ROWTYPE;
BEGIN
 PERFORM pulse_assert_upgrade_observation(NEW.observation_id);
 SELECT * INTO o FROM movie_upgrade_observation WHERE observation_id=NEW.observation_id;
 SELECT * INTO p FROM movie_upgrade_observation_permit WHERE observation_id=NEW.observation_id;
 IF NEW.observation_sha256 IS DISTINCT FROM sha256(convert_to(to_jsonb(o)::text,'UTF8'))
 OR NEW.permit_sha256 IS DISTINCT FROM sha256(convert_to(to_jsonb(p)::text,'UTF8'))
 OR NEW.returned_at<o.admitted_at OR NEW.recorded_at>clock_timestamp() THEN
  RAISE EXCEPTION 'upgrade observation result binding mismatch';
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_observation_immutable() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'upgrade observation history is immutable'; END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_observation_binding AFTER INSERT ON movie_upgrade_observation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_observation_permit_binding AFTER INSERT ON movie_upgrade_observation_permit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_observation_result_binding AFTER INSERT ON movie_upgrade_observation_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_result_guard();
CREATE TRIGGER movie_upgrade_observation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_observation FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER movie_upgrade_observation_permit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_observation_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER movie_upgrade_observation_result_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_observation_result FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_observation,movie_upgrade_observation_permit,movie_upgrade_observation_result FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_observation,movie_upgrade_observation_permit,movie_upgrade_observation_result,
 movie_upgrade_send_result,movie_upgrade_send_dispatch,movie_upgrade_send_authorization,movie_upgrade_preparation,
 movie_upgrade_preparation_lease,movie_upgrade_authority,acquisition_reservation,unit_reservation,download_requirement,
 subscription,policy,policy_revision,library_root,library_file,library_file_unit,unit_fact,media_unit,download_client,
 app_user,download_task,job,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_observation) OR EXISTS(SELECT 1 FROM movie_upgrade_observation_permit)
 OR EXISTS(SELECT 1 FROM movie_upgrade_observation_result)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action IN ('upgrade.observation-admit','upgrade.observation-result')) THEN
  RAISE EXCEPTION 'upgrade observation history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_observation_result;
DROP TABLE movie_upgrade_observation_permit;
DROP TABLE movie_upgrade_observation;
DROP FUNCTION pulse_upgrade_observation_immutable();
DROP FUNCTION pulse_upgrade_observation_result_guard();
DROP FUNCTION pulse_upgrade_observation_guard();
DROP FUNCTION pulse_assert_upgrade_observation(UUID);
DROP FUNCTION pulse_upgrade_observation_protected_digest(UUID);
DROP FUNCTION pulse_upgrade_observation_original_digest(UUID);
