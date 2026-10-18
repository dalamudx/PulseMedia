-- +goose Up
CREATE TABLE movie_upgrade_organization_job (
 job_id UUID PRIMARY KEY REFERENCES job(id) DEFERRABLE INITIALLY DEFERRED,
 command_id UUID NOT NULL UNIQUE CHECK(command_id<>'00000000-0000-0000-0000-000000000000'),
 operation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_preparation(operation_id),
 preparation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_organization_preparation(id),
 actor_id UUID NOT NULL REFERENCES app_user(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 26 AND 180 AND request_key LIKE 'upgrade.organization-queue:%'),
 preparation_snapshot JSONB NOT NULL CHECK(jsonb_typeof(preparation_snapshot)='object'),
 queued_job JSONB NOT NULL CHECK(jsonb_typeof(queued_job)='object'),
 commanded_at TIMESTAMPTZ NOT NULL CHECK(isfinite(commanded_at)),
 UNIQUE(actor_id,request_key)
);
CREATE TABLE movie_upgrade_organization_job_claim (
 job_id UUID PRIMARY KEY REFERENCES movie_upgrade_organization_job(job_id),
 claim_id UUID NOT NULL UNIQUE CHECK(claim_id<>'00000000-0000-0000-0000-000000000000'),
 claimed_job JSONB NOT NULL CHECK(jsonb_typeof(claimed_job)='object'),
 captured_at TIMESTAMPTZ NOT NULL CHECK(isfinite(captured_at))
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_organization_job_running(j job,b movie_upgrade_organization_job) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE(to_jsonb(j)=b.queued_job||jsonb_build_object('state','running','attempts',1,'lease_token',1,
 'lease_owner',j.lease_owner,'lease_until',j.lease_until,'updated_at',j.updated_at)
 AND length(j.lease_owner) BETWEEN 1 AND 130 AND isfinite(j.lease_until)
 AND j.lease_until>j.updated_at AND j.updated_at>=j.created_at,false)
$$;
CREATE FUNCTION pulse_assert_upgrade_organization_job(jid UUID,current_evidence BOOLEAN) RETURNS VOID LANGUAGE plpgsql AS $$
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
CREATE FUNCTION pulse_upgrade_organization_job_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='job' THEN
  IF NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_job WHERE job_id=NEW.id) THEN
   IF NEW.kind='movie_upgrade_organization_execute' THEN RAISE EXCEPTION 'upgrade organization command required' USING ERRCODE='23514'; END IF;
   RETURN NULL;
  END IF;
  PERFORM pulse_assert_upgrade_organization_job(NEW.id,false);
 ELSE PERFORM pulse_assert_upgrade_organization_job(NEW.job_id,true); END IF;
 RETURN NULL;
END $$;
CREATE FUNCTION pulse_upgrade_organization_job_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE j job%ROWTYPE; b movie_upgrade_organization_job%ROWTYPE;
BEGIN
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR UPDATE;
 SELECT * INTO STRICT b FROM movie_upgrade_organization_job WHERE job_id=NEW.job_id;
 IF NEW.claimed_job IS DISTINCT FROM to_jsonb(j) OR NOT pulse_upgrade_organization_job_running(j,b)
 OR NEW.captured_at>clock_timestamp() OR j.lease_until<=clock_timestamp()
 THEN RAISE EXCEPTION 'upgrade organization claim invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_organization_job_update() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
CREATE CONSTRAINT TRIGGER upgrade_organization_job_binding AFTER INSERT ON movie_upgrade_organization_job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_job_binding();
CREATE CONSTRAINT TRIGGER upgrade_organization_job_guard AFTER INSERT OR UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_job_binding();
CREATE CONSTRAINT TRIGGER upgrade_organization_claim_binding AFTER INSERT ON movie_upgrade_organization_job_claim DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_job_binding();
CREATE TRIGGER upgrade_organization_claim_capture BEFORE INSERT ON movie_upgrade_organization_job_claim FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_job_capture();
CREATE TRIGGER upgrade_organization_job_update BEFORE UPDATE OR DELETE ON job FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_job_update();
CREATE TRIGGER upgrade_organization_command_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_job FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER upgrade_organization_claim_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_job_claim FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_organization_job,movie_upgrade_organization_job_claim FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_organization_job,movie_upgrade_organization_job_claim,movie_upgrade_organization_preparation,
 movie_upgrade_organization_authorization,transfer_plan,transfer_step,job,app_user,subscription,policy,policy_revision,library_root,
 library_file,library_file_unit,unit_fact,media_unit,media,media_identity,wanted_item,download_client,acquisition_reservation,
 unit_reservation,download_requirement,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_organization_job) OR EXISTS(SELECT 1 FROM movie_upgrade_organization_job_claim)
 OR EXISTS(SELECT 1 FROM job WHERE kind='movie_upgrade_organization_execute')
 OR EXISTS(SELECT 1 FROM audit_log WHERE action IN ('upgrade.organization-queue','upgrade.organization-claim'))
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type IN ('upgrade.organization-queue','upgrade.organization-claim'))
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.organization-queue:%' OR idempotency_key LIKE 'upgrade.organization-claim:%')
 THEN RAISE EXCEPTION 'upgrade organization job history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER upgrade_organization_job_guard ON job;
DROP TRIGGER upgrade_organization_job_update ON job;
DROP FUNCTION pulse_upgrade_organization_job_running(job,movie_upgrade_organization_job);
DROP TABLE movie_upgrade_organization_job_claim;
DROP TABLE movie_upgrade_organization_job;
DROP FUNCTION pulse_upgrade_organization_job_update();
DROP FUNCTION pulse_upgrade_organization_job_capture();
DROP FUNCTION pulse_upgrade_organization_job_binding();
DROP FUNCTION pulse_assert_upgrade_organization_job(UUID,BOOLEAN);
