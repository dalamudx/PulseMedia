-- +goose Up
CREATE TABLE movie_upgrade_organization_execution_permit (
 operation_id UUID PRIMARY KEY REFERENCES movie_upgrade_organization_job(operation_id),
 permit_id UUID NOT NULL UNIQUE CHECK(permit_id<>'00000000-0000-0000-0000-000000000000'),
 job_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_organization_job_claim(job_id),
 claim_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_organization_job_claim(claim_id),
 actor_id UUID NOT NULL REFERENCES app_user(id),
 session_nonce UUID NOT NULL UNIQUE CHECK(session_nonce<>'00000000-0000-0000-0000-000000000000'),
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 backend_start TIMESTAMPTZ NOT NULL,
 advisory_key BIGINT NOT NULL,
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 observed_at TIMESTAMPTZ NOT NULL CHECK(isfinite(observed_at)),
 permitted_at TIMESTAMPTZ NOT NULL CHECK(isfinite(permitted_at)),
 lease_until TIMESTAMPTZ NOT NULL CHECK(isfinite(lease_until) AND lease_until>permitted_at AND lease_until<=permitted_at+interval '60 seconds')
);
CREATE TABLE movie_upgrade_organization_execution_result (
 permit_id UUID PRIMARY KEY REFERENCES movie_upgrade_organization_execution_permit(permit_id),
 job_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_organization_job(job_id),
 status TEXT NOT NULL CHECK(status IN ('linked_pending_ingestion','manual_review')),
 evidence JSONB NOT NULL CHECK(jsonb_typeof(evidence)='object' AND COALESCE(evidence->'physical_returned'='true'::jsonb,false)),
 returned_at TIMESTAMPTZ NOT NULL CHECK(isfinite(returned_at)),
 finished_job JSONB NOT NULL CHECK(jsonb_typeof(finished_job)='object')
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_organization_execution_finished(jid UUID,candidate JSONB) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM movie_upgrade_organization_execution_result r
 JOIN movie_upgrade_organization_execution_permit p ON p.permit_id=r.permit_id
 JOIN movie_upgrade_organization_job_claim c ON c.job_id=p.job_id
 WHERE r.job_id=jid AND p.job_id=jid AND p.claim_id=c.claim_id
 AND r.finished_job=c.claimed_job||jsonb_build_object('state','succeeded','lease_owner',NULL,'lease_until',NULL,
 'result',jsonb_build_object('organization_permit_id',p.permit_id,'status',r.status),'updated_at',r.returned_at,'finished_at',r.returned_at)
 AND candidate=r.finished_job)
$$;
CREATE FUNCTION pulse_upgrade_organization_execution_session(p movie_upgrade_organization_execution_permit) RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
 IF p.backend_pid<>pg_backend_pid()
 OR p.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 OR p.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_send_nonce',true)
 OR p.advisory_key IS DISTINCT FROM (('x'||substr(encode(sha256(convert_to('pulsemedia.movie-upgrade.physical.v1:'||p.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint)
 OR p.permitted_at>clock_timestamp() OR p.lease_until<=clock_timestamp()
 OR NOT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted
 AND mode='ExclusiveLock' AND objsubid=1 AND classid=((p.advisory_key>>32)&4294967295)::oid AND objid=(p.advisory_key&4294967295)::oid)
 THEN RAISE EXCEPTION 'upgrade organization physical session invalid' USING ERRCODE='23514'; END IF;
 PERFORM pg_advisory_xact_lock_shared(p.advisory_key);
 IF NOT pg_advisory_unlock(p.advisory_key) THEN RAISE EXCEPTION 'original exclusive organization session required' USING ERRCODE='23514'; END IF;
 IF NOT pg_try_advisory_lock(p.advisory_key) THEN RAISE EXCEPTION 'organization session restore failed' USING ERRCODE='23514'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_organization_execution_permit_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_organization_job%ROWTYPE; c movie_upgrade_organization_job_claim%ROWTYPE;
 p movie_upgrade_organization_preparation%ROWTYPE; j job%ROWTYPE;
BEGIN
 SELECT * INTO STRICT b FROM movie_upgrade_organization_job WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT c FROM movie_upgrade_organization_job_claim WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT p FROM movie_upgrade_organization_preparation WHERE id=b.preparation_id;
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR UPDATE;
 IF NEW.operation_id<>b.operation_id OR NEW.actor_id<>b.actor_id OR NEW.claim_id<>c.claim_id
 OR NEW.snapshot IS DISTINCT FROM jsonb_build_object('command',to_jsonb(b),'claim',to_jsonb(c),'preparation',to_jsonb(p))
 OR to_jsonb(j) IS DISTINCT FROM c.claimed_job OR j.state<>'running'
 OR NEW.observed_at>NEW.permitted_at OR NEW.permitted_at>=NEW.observed_at+interval '30 seconds'
 OR NEW.lease_until>j.lease_until
 THEN RAISE EXCEPTION 'upgrade organization physical binding invalid' USING ERRCODE='23514'; END IF;
 PERFORM pulse_upgrade_organization_execution_session(NEW);
 PERFORM pulse_assert_upgrade_organization_job(NEW.job_id,true);
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_organization_execution_result_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE p movie_upgrade_organization_execution_permit%ROWTYPE; c movie_upgrade_organization_job_claim%ROWTYPE; frozen JSONB;
BEGIN
 SELECT * INTO STRICT p FROM movie_upgrade_organization_execution_permit WHERE permit_id=NEW.permit_id FOR UPDATE;
 SELECT * INTO STRICT c FROM movie_upgrade_organization_job_claim WHERE job_id=p.job_id;
 frozen:=p.snapshot->'preparation'->'snapshot'->'physical';
 IF NEW.job_id<>p.job_id OR NEW.returned_at<p.permitted_at OR NEW.returned_at>clock_timestamp()
 OR NEW.finished_job IS DISTINCT FROM c.claimed_job||jsonb_build_object('state','succeeded','lease_owner',NULL,'lease_until',NULL,
 'result',jsonb_build_object('organization_permit_id',p.permit_id,'status',NEW.status),'updated_at',NEW.returned_at,'finished_at',NEW.returned_at)
 OR (NEW.status='linked_pending_ingestion' AND COALESCE(
 NEW.evidence->'source'=frozen->'source' AND NEW.evidence->'target'=frozen->'source'
 AND NEW.evidence->'old'=frozen->'old' AND NEW.evidence->'root'->'Device'=frozen->'root'->'Device'
 AND NEW.evidence->'root'->'Inode'=frozen->'root'->'Inode',false) IS NOT TRUE)
 THEN RAISE EXCEPTION 'upgrade organization physical result invalid' USING ERRCODE='23514'; END IF;
 PERFORM pulse_upgrade_organization_execution_session(p);
 PERFORM pulse_assert_upgrade_organization_job(p.job_id,true);
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_organization_execution_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pulse_assert_upgrade_organization_job(NEW.job_id,true);
 IF TG_TABLE_NAME='movie_upgrade_organization_execution_permit' THEN
  IF NEW.observed_at+interval '30 seconds'<=clock_timestamp() THEN RAISE EXCEPTION 'upgrade organization physical observation expired' USING ERRCODE='23514'; END IF;
 END IF;
 IF TG_TABLE_NAME='movie_upgrade_organization_execution_result' AND NOT pulse_upgrade_organization_execution_finished(NEW.job_id,(SELECT to_jsonb(j) FROM job j WHERE id=NEW.job_id))
 THEN RAISE EXCEPTION 'upgrade organization result requires atomic job success' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION pulse_assert_upgrade_organization_job(jid UUID,current_evidence BOOLEAN) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_organization_job%ROWTYPE; p movie_upgrade_organization_preparation%ROWTYPE; j job%ROWTYPE;
 c movie_upgrade_organization_job_claim%ROWTYPE; a movie_upgrade_organization_authorization%ROWTYPE;
BEGIN
 SELECT * INTO STRICT b FROM movie_upgrade_organization_job WHERE job_id=jid;
 SELECT * INTO STRICT p FROM movie_upgrade_organization_preparation WHERE id=b.preparation_id;
 SELECT * INTO STRICT j FROM job WHERE id=jid;
 SELECT * INTO STRICT a FROM movie_upgrade_organization_authorization WHERE id=p.authorization_id;
 IF b.operation_id<>p.operation_id OR b.actor_id<>p.actor_id OR b.preparation_snapshot IS DISTINCT FROM to_jsonb(p)
 OR j.kind<>'movie_upgrade_organization_execute' OR j.payload IS DISTINCT FROM jsonb_build_object('organization_job_id',jid)
 OR j.dedupe_key IS DISTINCT FROM 'movie-upgrade-organization:'||b.operation_id::text
 OR b.commanded_at IS DISTINCT FROM (b.queued_job->>'created_at')::timestamptz
 OR b.queued_job IS DISTINCT FROM to_jsonb(jsonb_populate_record(NULL::job,jsonb_build_object(
 'id',jid,'kind','movie_upgrade_organization_execute','dedupe_key','movie-upgrade-organization:'||b.operation_id::text,
 'payload',jsonb_build_object('organization_job_id',jid),'state','queued','priority',0,'attempts',0,'max_attempts',1,
 'lease_token',0,'not_before',b.commanded_at,'created_at',b.commanded_at,'updated_at',b.commanded_at)))
 THEN RAISE EXCEPTION 'upgrade organization job binding mismatch' USING ERRCODE='23514'; END IF;
 IF to_jsonb(j) IS DISTINCT FROM b.queued_job AND NOT pulse_upgrade_organization_job_running(j,b) AND j.state NOT IN ('dead','failed','succeeded')
 THEN RAISE EXCEPTION 'upgrade organization job state invalid' USING ERRCODE='23514'; END IF;
 IF j.state='succeeded' AND NOT pulse_upgrade_organization_execution_finished(jid,to_jsonb(j)) THEN RAISE EXCEPTION 'upgrade organization trusted result required' USING ERRCODE='23514'; END IF;
 IF current_evidence THEN
  PERFORM pulse_assert_upgrade_organization_preparation(b.operation_id);
  IF pulse_upgrade_organization_basis(b.operation_id,a.observation_id) IS DISTINCT FROM a.snapshot
  THEN RAISE EXCEPTION 'upgrade organization job evidence changed' USING ERRCODE='23514'; END IF;
 END IF;
 SELECT * INTO c FROM movie_upgrade_organization_job_claim WHERE job_id=jid;
 IF FOUND AND (NOT pulse_upgrade_organization_job_running(jsonb_populate_record(NULL::job,c.claimed_job),b)
 OR c.captured_at<(c.claimed_job->>'updated_at')::timestamptz OR c.captured_at>=(c.claimed_job->>'lease_until')::timestamptz
 OR (j.state='running' AND to_jsonb(j) IS DISTINCT FROM c.claimed_job))
 THEN RAISE EXCEPTION 'upgrade organization original claim changed' USING ERRCODE='23514'; END IF;
 IF current_evidence AND c.job_id IS NOT NULL AND (j.state NOT IN ('running','succeeded') OR (c.claimed_job->>'lease_until')::timestamptz<=clock_timestamp())
 THEN RAISE EXCEPTION 'upgrade organization lease expired' USING ERRCODE='23514'; END IF;
END $$;
CREATE OR REPLACE FUNCTION pulse_upgrade_organization_job_update() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_organization_job%ROWTYPE; expected JSONB;
BEGIN
 SELECT * INTO b FROM movie_upgrade_organization_job WHERE job_id=OLD.id;
 IF NOT FOUND THEN
  IF OLD.kind='movie_upgrade_organization_execute' THEN RAISE EXCEPTION 'upgrade organization command required'; END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'upgrade organization history immutable'; END IF;
 IF to_jsonb(NEW)=to_jsonb(OLD) THEN RETURN NEW; END IF;
 IF to_jsonb(OLD)=b.queued_job AND pulse_upgrade_organization_job_running(NEW,b) THEN RETURN NEW; END IF;
 IF OLD.state='running' AND NEW.state='succeeded' AND pulse_upgrade_organization_execution_finished(OLD.id,to_jsonb(NEW)) THEN RETURN NEW; END IF;
 expected:=to_jsonb(OLD)||jsonb_build_object('state',NEW.state,'lease_owner',NULL,'lease_until',NULL,
 'error',NEW.error,'not_before',NEW.not_before,'updated_at',NEW.updated_at,'finished_at',NEW.finished_at);
 IF (to_jsonb(OLD)=b.queued_job OR pulse_upgrade_organization_job_running(OLD,b))
 AND NEW.state IN ('dead','failed') AND NEW.updated_at>=OLD.updated_at
 AND (NEW.finished_at IS NULL OR NEW.finished_at=NEW.updated_at) AND to_jsonb(NEW)=expected THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'upgrade organization claim frozen' USING ERRCODE='23514';
END $$;
-- +goose StatementEnd
CREATE TRIGGER upgrade_organization_execution_permit_guard BEFORE INSERT ON movie_upgrade_organization_execution_permit FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_execution_permit_guard();
CREATE TRIGGER upgrade_organization_execution_result_guard BEFORE INSERT ON movie_upgrade_organization_execution_result FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_execution_result_guard();
CREATE CONSTRAINT TRIGGER upgrade_organization_execution_permit_binding AFTER INSERT ON movie_upgrade_organization_execution_permit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_execution_binding();
CREATE CONSTRAINT TRIGGER upgrade_organization_execution_result_binding AFTER INSERT ON movie_upgrade_organization_execution_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_execution_binding();
CREATE TRIGGER upgrade_organization_execution_permit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_execution_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER upgrade_organization_execution_result_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_execution_result FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_organization_execution_permit,movie_upgrade_organization_execution_result FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_organization_execution_permit,movie_upgrade_organization_execution_result,movie_upgrade_organization_job,movie_upgrade_organization_job_claim,
 movie_upgrade_organization_preparation,movie_upgrade_organization_authorization,transfer_plan,transfer_step,job,app_user,subscription,policy,policy_revision,
 library_root,library_file,library_file_unit,unit_fact,media_unit,media,media_identity,wanted_item,download_client,acquisition_reservation,
 unit_reservation,download_requirement,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_organization_execution_permit) OR EXISTS(SELECT 1 FROM movie_upgrade_organization_execution_result)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action IN ('upgrade.organization-physical-permit','upgrade.organization-physical-result'))
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type IN ('upgrade.organization-physical-permit','upgrade.organization-physical-result'))
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.organization-physical-permit:%' OR idempotency_key LIKE 'upgrade.organization-physical-result:%')
 THEN RAISE EXCEPTION 'upgrade organization physical history prevents downgrade'; END IF;
END $$;
CREATE OR REPLACE FUNCTION pulse_assert_upgrade_organization_job(jid UUID,current_evidence BOOLEAN) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_organization_job%ROWTYPE; p movie_upgrade_organization_preparation%ROWTYPE; j job%ROWTYPE;
 c movie_upgrade_organization_job_claim%ROWTYPE; a movie_upgrade_organization_authorization%ROWTYPE;
BEGIN
 SELECT * INTO STRICT b FROM movie_upgrade_organization_job WHERE job_id=jid;
 SELECT * INTO STRICT p FROM movie_upgrade_organization_preparation WHERE id=b.preparation_id;
 SELECT * INTO STRICT j FROM job WHERE id=jid;
 SELECT * INTO STRICT a FROM movie_upgrade_organization_authorization WHERE id=p.authorization_id;
 IF b.operation_id<>p.operation_id OR b.actor_id<>p.actor_id OR b.preparation_snapshot IS DISTINCT FROM to_jsonb(p)
 OR j.kind<>'movie_upgrade_organization_execute' OR j.payload IS DISTINCT FROM jsonb_build_object('organization_job_id',jid)
 OR j.dedupe_key IS DISTINCT FROM 'movie-upgrade-organization:'||b.operation_id::text
 OR b.commanded_at IS DISTINCT FROM (b.queued_job->>'created_at')::timestamptz
 OR b.queued_job IS DISTINCT FROM to_jsonb(jsonb_populate_record(NULL::job,jsonb_build_object(
 'id',jid,'kind','movie_upgrade_organization_execute','dedupe_key','movie-upgrade-organization:'||b.operation_id::text,
 'payload',jsonb_build_object('organization_job_id',jid),'state','queued','priority',0,'attempts',0,'max_attempts',1,
 'lease_token',0,'not_before',b.commanded_at,'created_at',b.commanded_at,'updated_at',b.commanded_at)))
 THEN RAISE EXCEPTION 'upgrade organization job binding mismatch' USING ERRCODE='23514'; END IF;
 IF to_jsonb(j) IS DISTINCT FROM b.queued_job AND NOT pulse_upgrade_organization_job_running(j,b) AND j.state NOT IN ('dead','failed')
 THEN RAISE EXCEPTION 'upgrade organization job state invalid' USING ERRCODE='23514'; END IF;
 IF current_evidence THEN
  PERFORM pulse_assert_upgrade_organization_preparation(b.operation_id);
  IF pulse_upgrade_organization_basis(b.operation_id,a.observation_id) IS DISTINCT FROM a.snapshot
  THEN RAISE EXCEPTION 'upgrade organization job evidence changed' USING ERRCODE='23514'; END IF;
 END IF;
 SELECT * INTO c FROM movie_upgrade_organization_job_claim WHERE job_id=jid;
 IF FOUND AND (NOT pulse_upgrade_organization_job_running(jsonb_populate_record(NULL::job,c.claimed_job),b)
 OR c.captured_at<(c.claimed_job->>'updated_at')::timestamptz OR c.captured_at>=(c.claimed_job->>'lease_until')::timestamptz
 OR (j.state='running' AND to_jsonb(j) IS DISTINCT FROM c.claimed_job))
 THEN RAISE EXCEPTION 'upgrade organization original claim changed' USING ERRCODE='23514'; END IF;
 IF current_evidence AND c.job_id IS NOT NULL AND (j.state<>'running' OR j.lease_until<=clock_timestamp())
 THEN RAISE EXCEPTION 'upgrade organization lease expired' USING ERRCODE='23514'; END IF;
END $$;
CREATE OR REPLACE FUNCTION pulse_upgrade_organization_job_update() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_organization_job%ROWTYPE; expected JSONB;
BEGIN
 SELECT * INTO b FROM movie_upgrade_organization_job WHERE job_id=OLD.id;
 IF NOT FOUND THEN
  IF OLD.kind='movie_upgrade_organization_execute' THEN RAISE EXCEPTION 'upgrade organization command required'; END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'upgrade organization history immutable'; END IF;
 IF to_jsonb(NEW)=to_jsonb(OLD) THEN RETURN NEW; END IF;
 IF to_jsonb(OLD)=b.queued_job AND pulse_upgrade_organization_job_running(NEW,b) THEN RETURN NEW; END IF;
 expected:=to_jsonb(OLD)||jsonb_build_object('state',NEW.state,'lease_owner',NULL,'lease_until',NULL,
 'error',NEW.error,'not_before',NEW.not_before,'updated_at',NEW.updated_at,'finished_at',NEW.finished_at);
 IF (to_jsonb(OLD)=b.queued_job OR pulse_upgrade_organization_job_running(OLD,b))
 AND NEW.state IN ('dead','failed') AND NEW.updated_at>=OLD.updated_at
 AND (NEW.finished_at IS NULL OR NEW.finished_at=NEW.updated_at) AND to_jsonb(NEW)=expected THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'upgrade organization claim frozen' USING ERRCODE='23514';
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_organization_execution_result;
DROP FUNCTION pulse_upgrade_organization_execution_result_guard();
DROP FUNCTION pulse_upgrade_organization_execution_finished(UUID,JSONB);
DROP TRIGGER upgrade_organization_execution_permit_guard ON movie_upgrade_organization_execution_permit;
DROP FUNCTION pulse_upgrade_organization_execution_permit_guard();
DROP FUNCTION pulse_upgrade_organization_execution_session(movie_upgrade_organization_execution_permit);
DROP TABLE movie_upgrade_organization_execution_permit;
DROP FUNCTION pulse_upgrade_organization_execution_binding();
