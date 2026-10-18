-- +goose Up
-- +goose StatementBegin
-- Keep the accepted unknown-only R3 definition unchanged. Its confirmed sibling
-- preserves the same original-task, dispatch, return and protection checks.
DO $$ DECLARE src TEXT; BEGIN
 src:=pg_get_functiondef('pulse_upgrade_observation_original_digest(uuid)'::regprocedure);
 IF position('r.status=''unknown''' IN src)=0 THEN RAISE EXCEPTION 'upgrade tracking original digest source changed'; END IF;
 src:=replace(src,'pulse_upgrade_observation_original_digest','pulse_upgrade_tracking_original_digest');
 EXECUTE replace(src,'r.status=''unknown''','r.status=''confirmed''');
END $$;

CREATE TABLE movie_upgrade_tracking_observation (
 observation_id UUID PRIMARY KEY, operation_id UUID NOT NULL REFERENCES movie_upgrade_preparation(operation_id),
 task_id UUID NOT NULL REFERENCES download_task(id), actor_id UUID NOT NULL REFERENCES app_user(id),
 owner UUID NOT NULL, token UUID NOT NULL, session_nonce UUID NOT NULL,
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0), backend_start TIMESTAMPTZ NOT NULL,
 advisory_key BIGINT NOT NULL, admitted_at TIMESTAMPTZ NOT NULL, lease_until TIMESTAMPTZ NOT NULL,
 original_sha256 BYTEA NOT NULL CHECK(octet_length(original_sha256)=32),
 protected_sha256 BYTEA NOT NULL CHECK(octet_length(protected_sha256)=32),
 descriptor JSONB NOT NULL CHECK(jsonb_typeof(descriptor)='object'),
 prior_observation_id UUID, request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 200),
 UNIQUE NULLS NOT DISTINCT(operation_id,prior_observation_id), UNIQUE(operation_id,request_key),
 CHECK(isfinite(admitted_at) AND lease_until=admitted_at+INTERVAL '60 seconds'),
 CHECK(prior_observation_id IS DISTINCT FROM observation_id),
 CHECK(NOT ('00000000-0000-0000-0000-000000000000'::UUID=ANY(ARRAY[observation_id,operation_id,task_id,actor_id,owner,token,session_nonce]))),
 CHECK(prior_observation_id IS NULL OR prior_observation_id<>'00000000-0000-0000-0000-000000000000'::UUID)
);
CREATE INDEX movie_upgrade_tracking_operation ON movie_upgrade_tracking_observation(operation_id,admitted_at);
CREATE TABLE movie_upgrade_tracking_permit (
 observation_id UUID PRIMARY KEY REFERENCES movie_upgrade_tracking_observation(observation_id),
 operation_id UUID NOT NULL REFERENCES movie_upgrade_preparation(operation_id), owner UUID NOT NULL,
 token UUID NOT NULL, session_nonce UUID NOT NULL, backend_pid INTEGER NOT NULL, backend_start TIMESTAMPTZ NOT NULL,
 advisory_key BIGINT NOT NULL, issued_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE movie_upgrade_tracking_result (
 observation_id UUID PRIMARY KEY REFERENCES movie_upgrade_tracking_permit(observation_id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 200),
 outcome TEXT NOT NULL CHECK(outcome IN ('tracking','completed','manual_review')),
 reason TEXT NOT NULL CHECK(reason~'^[a-z_]{1,64}$'),
 returned_at TIMESTAMPTZ NOT NULL CHECK(isfinite(returned_at)), recorded_at TIMESTAMPTZ NOT NULL CHECK(isfinite(recorded_at)),
 observation_sha256 BYTEA NOT NULL CHECK(octet_length(observation_sha256)=32),
 permit_sha256 BYTEA NOT NULL CHECK(octet_length(permit_sha256)=32), evidence JSONB NOT NULL CHECK(jsonb_typeof(evidence)='object'),
 disposition TEXT NOT NULL,
 CHECK(disposition=CASE outcome WHEN 'tracking' THEN 'tracking' WHEN 'completed' THEN 'organization_review_required' ELSE 'manual_review' END)
);
ALTER TABLE movie_upgrade_tracking_observation ADD CONSTRAINT movie_upgrade_tracking_prior_fk
 FOREIGN KEY(prior_observation_id) REFERENCES movie_upgrade_tracking_result(observation_id);

CREATE FUNCTION pulse_upgrade_tracking_session_held(o movie_upgrade_tracking_observation) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
BEGIN
 IF o.backend_pid IS DISTINCT FROM pg_backend_pid()
 OR o.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 OR o.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_tracking_nonce',true)
 OR o.advisory_key IS DISTINCT FROM (('x'||substr(encode(sha256(convert_to('pulsemedia.movie-upgrade.physical.v1:'||o.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint)
 OR NOT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock'
 AND objsubid=1 AND classid=((o.advisory_key>>32)&4294967295)::oid AND objid=(o.advisory_key&4294967295)::oid) THEN RETURN FALSE; END IF;
 -- An xact-only lock is insufficient. Hold the shared transaction lock while
 -- probing/reacquiring our exact exclusive session lock, without an unlocked gap.
 PERFORM pg_advisory_xact_lock_shared(o.advisory_key);
 IF NOT pg_advisory_unlock(o.advisory_key) THEN RETURN FALSE; END IF;
 RETURN pg_try_advisory_lock(o.advisory_key);
END $$;

CREATE FUNCTION pulse_assert_upgrade_tracking(obs UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE o movie_upgrade_tracking_observation%ROWTYPE; p movie_upgrade_preparation%ROWTYPE;
 q movie_upgrade_tracking_permit%ROWTYPE; r movie_upgrade_tracking_result%ROWTYPE;
 expected JSONB; client UUID; now_at TIMESTAMPTZ;
BEGIN
 SELECT * INTO STRICT o FROM movie_upgrade_tracking_observation WHERE observation_id=obs FOR SHARE;
 SELECT * INTO STRICT p FROM movie_upgrade_preparation WHERE operation_id=o.operation_id FOR UPDATE;
 PERFORM pulse_assert_upgrade_preparation(o.operation_id);
 SELECT client_id INTO STRICT client FROM movie_upgrade_authority WHERE operation_id=o.operation_id;
 IF o.original_sha256 IS DISTINCT FROM pulse_upgrade_tracking_original_digest(o.operation_id)
 OR o.protected_sha256 IS DISTINCT FROM pulse_upgrade_observation_protected_digest(o.operation_id)
 OR o.task_id IS DISTINCT FROM p.task_id OR o.actor_id IS DISTINCT FROM p.actor_id THEN
 RAISE EXCEPTION 'upgrade tracking original graph mismatch'; END IF;
 expected:=jsonb_build_object('identity',jsonb_build_object('OperationID',o.operation_id,'TaskID',o.task_id,
 'ClientID',client,'ClientDigest',p.evidence->>'client_digest','InfoHash',p.evidence->'proof'->>'info_hash',
 'OperationTag','pulsemedia:op:'||o.operation_id::text,'SavePath',p.evidence->>'save_path',
 'TotalBytes',(p.evidence->'proof'->>'total_bytes')::bigint),'target',p.evidence->'proof'->'target','files',p.evidence->'proof'->'files');
 IF o.descriptor IS DISTINCT FROM expected THEN RAISE EXCEPTION 'upgrade tracking descriptor mismatch'; END IF;
 SELECT * INTO STRICT q FROM movie_upgrade_tracking_permit WHERE observation_id=obs;
 IF ROW(q.operation_id,q.owner,q.token,q.session_nonce,q.backend_pid,q.backend_start,q.advisory_key,q.issued_at)
 IS DISTINCT FROM ROW(o.operation_id,o.owner,o.token,o.session_nonce,o.backend_pid,o.backend_start,o.advisory_key,o.admitted_at) THEN
 RAISE EXCEPTION 'upgrade tracking permit mismatch'; END IF;
 IF o.prior_observation_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM movie_upgrade_tracking_observation prior JOIN movie_upgrade_tracking_result result USING(observation_id)
 WHERE prior.observation_id=o.prior_observation_id AND prior.operation_id=o.operation_id
 AND result.outcome='tracking' AND result.recorded_at<=o.admitted_at-INTERVAL '30 seconds') THEN
 RAISE EXCEPTION 'upgrade tracking predecessor or cooldown mismatch'; END IF;
 IF EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation other WHERE other.operation_id=o.operation_id
 AND other.observation_id<>obs AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_result result WHERE result.observation_id=other.observation_id)) THEN
 RAISE EXCEPTION 'upgrade tracking outstanding physical permit'; END IF;
 SELECT * INTO r FROM movie_upgrade_tracking_result WHERE observation_id=obs;
 IF FOUND THEN
 expected:=jsonb_build_object('version','movie-upgrade.tracking.v1');
 IF r.outcome='completed' THEN expected:=expected||jsonb_build_object('info_hash',p.evidence->'proof'->>'info_hash',
 'total_bytes',(p.evidence->'proof'->>'total_bytes')::bigint,'files',p.evidence->'proof'->'files'); END IF;
 IF r.returned_at<o.admitted_at OR r.returned_at>r.recorded_at OR r.recorded_at>clock_timestamp() OR r.recorded_at>=o.lease_until
 OR r.observation_sha256 IS DISTINCT FROM sha256(convert_to(to_jsonb(o)::text,'UTF8'))
 OR r.permit_sha256 IS DISTINCT FROM sha256(convert_to(to_jsonb(q)::text,'UTF8')) OR r.evidence IS DISTINCT FROM expected THEN
 RAISE EXCEPTION 'upgrade tracking returned evidence mismatch'; END IF;
 END IF;
 -- Check the database clock after all original graph/protection/result reads.
 IF NOT pulse_upgrade_tracking_session_held(o) THEN RAISE EXCEPTION 'upgrade tracking original session required'; END IF;
 now_at:=clock_timestamp();
 IF o.admitted_at>now_at OR o.lease_until<=now_at THEN
 RAISE EXCEPTION 'upgrade tracking lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_tracking_lease'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_tracking_constraint() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN PERFORM pulse_assert_upgrade_tracking(NEW.observation_id); RETURN NEW; END $$;
DO $$ DECLARE t TEXT; BEGIN
 FOREACH t IN ARRAY ARRAY['movie_upgrade_tracking_observation','movie_upgrade_tracking_permit','movie_upgrade_tracking_result'] LOOP
 EXECUTE format('CREATE TRIGGER tracking_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_immutable()',t);
 EXECUTE format('CREATE CONSTRAINT TRIGGER tracking_assert AFTER INSERT ON %I DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_tracking_constraint()',t);
 END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
LOCK TABLE movie_upgrade_authority,movie_upgrade_preparation,movie_upgrade_send_authorization,movie_upgrade_send_dispatch,movie_upgrade_send_result,
 download_task,job,movie_upgrade_tracking_observation,movie_upgrade_tracking_permit,movie_upgrade_tracking_result,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation) OR EXISTS(SELECT 1 FROM movie_upgrade_tracking_permit)
 OR EXISTS(SELECT 1 FROM movie_upgrade_tracking_result) OR EXISTS(SELECT 1 FROM audit_log WHERE action LIKE 'upgrade.tracking-%')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type LIKE 'upgrade.tracking-%')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.tracking-%') THEN
 RAISE EXCEPTION 'upgrade tracking history prevents downgrade'; END IF;
END $$;
DROP TRIGGER tracking_assert ON movie_upgrade_tracking_result;
DROP TRIGGER tracking_assert ON movie_upgrade_tracking_permit;
DROP TRIGGER tracking_assert ON movie_upgrade_tracking_observation;
DROP TRIGGER tracking_immutable ON movie_upgrade_tracking_result;
DROP TRIGGER tracking_immutable ON movie_upgrade_tracking_permit;
DROP TRIGGER tracking_immutable ON movie_upgrade_tracking_observation;
DROP FUNCTION pulse_upgrade_tracking_constraint();
DROP FUNCTION pulse_assert_upgrade_tracking(UUID);
DROP FUNCTION pulse_upgrade_tracking_session_held(movie_upgrade_tracking_observation);
ALTER TABLE movie_upgrade_tracking_observation DROP CONSTRAINT movie_upgrade_tracking_prior_fk;
DROP TABLE movie_upgrade_tracking_result;
DROP TABLE movie_upgrade_tracking_permit;
DROP TABLE movie_upgrade_tracking_observation;
DROP FUNCTION pulse_upgrade_tracking_original_digest(UUID);
-- +goose StatementEnd
