-- +goose Up
CREATE TABLE movie_upgrade_tracking_job (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 operation_id UUID NOT NULL REFERENCES movie_upgrade_preparation(operation_id),
 task_id UUID NOT NULL REFERENCES download_task(id), actor_id UUID NOT NULL REFERENCES app_user(id),
 prior_observation_id UUID REFERENCES movie_upgrade_tracking_result(observation_id),
 queued_job JSONB NOT NULL CHECK(jsonb_typeof(queued_job)='object'),
 UNIQUE NULLS NOT DISTINCT(operation_id,prior_observation_id)
);
CREATE TABLE movie_upgrade_tracking_job_claim (
 job_id UUID PRIMARY KEY REFERENCES movie_upgrade_tracking_job(job_id),
 observation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_tracking_observation(observation_id) DEFERRABLE INITIALLY DEFERRED,
 binding_sha256 BYTEA NOT NULL CHECK(octet_length(binding_sha256)=32),
 admission_key TEXT NOT NULL, result_key TEXT NOT NULL,
 claimed_job JSONB NOT NULL CHECK(jsonb_typeof(claimed_job)='object')
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_tracking_job_running(j job,b movie_upgrade_tracking_job) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE(to_jsonb(j)=b.queued_job||jsonb_build_object('state','running','attempts',1,
 'lease_token',1,'lease_owner',j.lease_owner,'lease_until',j.lease_until,'updated_at',j.updated_at)
 AND length(j.lease_owner) BETWEEN 1 AND 130 AND j.lease_until>j.updated_at AND j.updated_at>=j.created_at,false)
$$;
CREATE FUNCTION pulse_upgrade_tracking_job_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE j job%ROWTYPE; p movie_upgrade_preparation%ROWTYPE;
BEGIN
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR UPDATE;
 SELECT * INTO STRICT p FROM movie_upgrade_preparation WHERE operation_id=NEW.operation_id;
 IF to_jsonb(j) IS DISTINCT FROM NEW.queued_job OR j.kind<>'movie_upgrade_tracking'
 OR j.payload IS DISTINCT FROM jsonb_build_object('tracking_job_id',j.id)
 OR j.dedupe_key IS DISTINCT FROM 'movie-upgrade-tracking:'||NEW.operation_id::text||':'||COALESCE(NEW.prior_observation_id::text,'first')
 OR NEW.task_id<>p.task_id OR NEW.actor_id<>p.actor_id OR j.state<>'queued' OR j.attempts<>0 OR j.max_attempts<>1
 OR j.priority<>0 OR j.lease_owner IS NOT NULL OR j.lease_until IS NOT NULL OR j.lease_token<>0
 OR j.result IS NOT NULL OR j.error IS NOT NULL OR j.finished_at IS NOT NULL
 OR j.created_at<>j.updated_at OR j.not_before<>j.created_at
 OR pulse_upgrade_tracking_original_digest(NEW.operation_id) IS NULL THEN RAISE EXCEPTION 'invalid tracking queued binding'; END IF;
 IF NEW.prior_observation_id IS NULL THEN
  IF EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation WHERE operation_id=NEW.operation_id) THEN RAISE EXCEPTION 'tracking already started'; END IF;
 ELSE
  IF NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id)
   WHERE o.observation_id=NEW.prior_observation_id AND o.operation_id=NEW.operation_id AND r.outcome='tracking'
   AND r.recorded_at<=clock_timestamp()-interval '30 seconds'
   AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=o.observation_id)) THEN
   RAISE EXCEPTION 'tracking predecessor not ready'; END IF;
 END IF;
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_tracking_terminal_job(obs UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT c.claimed_job||jsonb_build_object('state','succeeded','result',jsonb_build_object(
 'operation_id',o.operation_id,'task_id',o.task_id,'observation_id',o.observation_id,
 'outcome',r.outcome,'reason',r.reason,'disposition',r.disposition,'execution_allowed',false),
 'lease_until',NULL,'updated_at',r.recorded_at,'finished_at',r.recorded_at)
 FROM movie_upgrade_tracking_job_claim c JOIN movie_upgrade_tracking_observation o USING(observation_id)
 JOIN movie_upgrade_tracking_result r USING(observation_id) WHERE c.observation_id=obs AND r.request_key=c.result_key
$$;
CREATE FUNCTION pulse_assert_upgrade_tracking_job(obs UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE o movie_upgrade_tracking_observation%ROWTYPE; b movie_upgrade_tracking_job%ROWTYPE;
 c movie_upgrade_tracking_job_claim%ROWTYPE; j job%ROWTYPE; terminal JSONB;
BEGIN
 SELECT * INTO STRICT o FROM movie_upgrade_tracking_observation WHERE observation_id=obs;
 SELECT * INTO b FROM movie_upgrade_tracking_job WHERE operation_id=o.operation_id AND prior_observation_id IS NOT DISTINCT FROM o.prior_observation_id;
 IF NOT FOUND THEN
  IF EXISTS(SELECT 1 FROM movie_upgrade_tracking_job_claim WHERE observation_id=obs) THEN RAISE EXCEPTION 'tracking job missing'; END IF;
  RETURN;
 END IF;
 SELECT * INTO STRICT c FROM movie_upgrade_tracking_job_claim WHERE job_id=b.job_id;
 SELECT * INTO STRICT j FROM job WHERE id=b.job_id FOR UPDATE;
 IF c.observation_id<>obs OR o.task_id<>b.task_id OR o.actor_id<>b.actor_id OR o.owner<>b.job_id
 OR c.admission_key IS DISTINCT FROM 'upgrade-track-admit:'||b.job_id::text
 OR c.result_key IS DISTINCT FROM 'upgrade-track-result:'||b.job_id::text OR o.request_key<>c.admission_key
 OR c.binding_sha256 IS DISTINCT FROM sha256(convert_to(to_jsonb(b)::text,'UTF8'))
 OR NOT pulse_upgrade_tracking_job_running(jsonb_populate_record(NULL::job,c.claimed_job),b)
 THEN RAISE EXCEPTION 'tracking job claim mismatch'; END IF;
 terminal:=pulse_upgrade_tracking_terminal_job(obs);
 IF terminal IS NULL THEN
  IF to_jsonb(j) IS DISTINCT FROM c.claimed_job THEN RAISE EXCEPTION 'tracking running claim changed'; END IF;
 ELSE
  IF to_jsonb(j) IS DISTINCT FROM terminal THEN RAISE EXCEPTION 'tracking result and job disagree'; END IF;
 END IF;
 IF (c.claimed_job->>'lease_until')::timestamptz IS NULL OR (c.claimed_job->>'lease_until')::timestamptz<=clock_timestamp()
 THEN RAISE EXCEPTION 'tracking job lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_tracking_job_lease'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_tracking_claim_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE actual JSONB;
BEGIN
 SELECT to_jsonb(j) INTO STRICT actual FROM job j WHERE id=NEW.job_id FOR UPDATE;
 IF actual IS DISTINCT FROM NEW.claimed_job THEN RAISE EXCEPTION 'tracking original claim changed'; END IF;
 RETURN NEW;
END $$;
-- Keep the schema72 implementation verbatim under a private name; the wrapper
-- adds job fencing without weakening any original physical/session checks.
DO $$ DECLARE src TEXT; BEGIN
 src:=pg_get_functiondef('pulse_assert_upgrade_tracking(uuid)'::regprocedure);
 EXECUTE replace(src,'pulse_assert_upgrade_tracking','pulse_assert_upgrade_tracking_base72');
END $$;
CREATE OR REPLACE FUNCTION pulse_assert_upgrade_tracking(obs UUID) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE expiry TIMESTAMPTZ;
BEGIN
 PERFORM pulse_assert_upgrade_tracking_job(obs);
 PERFORM pulse_assert_upgrade_tracking_base72(obs);
 SELECT (claimed_job->>'lease_until')::timestamptz INTO expiry FROM movie_upgrade_tracking_job_claim WHERE observation_id=obs;
 IF FOUND AND (expiry IS NULL OR expiry<=clock_timestamp()) THEN
  RAISE EXCEPTION 'tracking job lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_tracking_job_lease'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_tracking_job_update() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_tracking_job%ROWTYPE; c movie_upgrade_tracking_job_claim%ROWTYPE; expected JSONB;
BEGIN
 SELECT * INTO b FROM movie_upgrade_tracking_job WHERE job_id=OLD.id;
 IF NOT FOUND THEN
  IF OLD.kind='movie_upgrade_tracking' THEN RAISE EXCEPTION 'tracking job binding required'; END IF;
IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'tracking job history is immutable'; END IF;
 IF to_jsonb(NEW)=to_jsonb(OLD) THEN RETURN NEW; END IF;
 IF to_jsonb(OLD)=b.queued_job AND pulse_upgrade_tracking_job_running(NEW,b) THEN RETURN NEW; END IF;
 SELECT * INTO c FROM movie_upgrade_tracking_job_claim WHERE job_id=b.job_id;
 IF FOUND AND EXISTS(SELECT 1 FROM movie_upgrade_tracking_result WHERE observation_id=c.observation_id) THEN
  expected:=pulse_upgrade_tracking_terminal_job(c.observation_id);
  IF to_jsonb(OLD)=c.claimed_job AND to_jsonb(NEW)=expected THEN RETURN NEW; END IF;
 ELSE
  expected:=to_jsonb(OLD)||jsonb_build_object('state',NEW.state,'lease_owner',NULL,'lease_until',NULL,
   'error',NEW.error,'not_before',NEW.not_before,'updated_at',NEW.updated_at,'finished_at',NEW.finished_at);
  IF pulse_upgrade_tracking_job_running(OLD,b) AND (c.job_id IS NULL OR to_jsonb(OLD)=c.claimed_job)
   AND NEW.state IN ('dead','failed') AND NEW.lease_owner IS NULL AND NEW.lease_until IS NULL
   AND NEW.updated_at>=OLD.updated_at AND (NEW.finished_at IS NULL OR NEW.finished_at=NEW.updated_at)
   AND to_jsonb(NEW)=expected THEN RETURN NEW; END IF;
 END IF;
 RAISE EXCEPTION 'tracking job claim is frozen' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_tracking_job_frozen';
END $$;
CREATE FUNCTION pulse_upgrade_tracking_job_constraint() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_tracking_job%ROWTYPE; obs UUID; actual JSONB;
BEGIN
 IF TG_TABLE_NAME='job' THEN
  SELECT * INTO b FROM movie_upgrade_tracking_job WHERE job_id=NEW.id;
  IF NOT FOUND THEN
   IF NEW.kind='movie_upgrade_tracking' THEN RAISE EXCEPTION 'tracking job binding required'; END IF;
   RETURN NULL;
  END IF;
  SELECT to_jsonb(j) INTO STRICT actual FROM job j WHERE id=b.job_id;
  SELECT observation_id INTO obs FROM movie_upgrade_tracking_job_claim WHERE job_id=b.job_id;
  IF obs IS NULL THEN
   IF actual<>b.queued_job AND NOT pulse_upgrade_tracking_job_running(jsonb_populate_record(NULL::job,actual),b)
    AND actual->>'state' NOT IN ('dead','failed') THEN RAISE EXCEPTION 'tracking job changed before admission'; END IF;
   RETURN NULL;
  END IF;
  -- A failed/dead job keeps its permit and does not authorize another query.
  IF actual->>'state' IN ('dead','failed') AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_result WHERE observation_id=obs) THEN RETURN NULL; END IF;
 ELSE obs:=NEW.observation_id; END IF;
 PERFORM pulse_assert_upgrade_tracking(obs);
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER tracking_job_capture BEFORE INSERT ON movie_upgrade_tracking_job FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_tracking_job_capture();
CREATE TRIGGER tracking_job_immutable BEFORE UPDATE OR DELETE ON movie_upgrade_tracking_job FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER tracking_claim_capture BEFORE INSERT ON movie_upgrade_tracking_job_claim FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_tracking_claim_capture();
CREATE TRIGGER tracking_claim_immutable BEFORE UPDATE OR DELETE ON movie_upgrade_tracking_job_claim FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER tracking_job_update BEFORE UPDATE OR DELETE ON job FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_tracking_job_update();
CREATE CONSTRAINT TRIGGER tracking_job_assert AFTER INSERT OR UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_tracking_job_constraint();
CREATE CONSTRAINT TRIGGER tracking_claim_assert AFTER INSERT ON movie_upgrade_tracking_job_claim DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_tracking_job_constraint();

-- +goose Down
-- +goose StatementBegin
LOCK TABLE movie_upgrade_tracking_job,movie_upgrade_tracking_job_claim,movie_upgrade_tracking_observation,movie_upgrade_tracking_permit,movie_upgrade_tracking_result,
 movie_upgrade_authority,movie_upgrade_preparation,movie_upgrade_send_authorization,movie_upgrade_send_dispatch,movie_upgrade_send_result,
 download_task,job,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_tracking_job) OR EXISTS(SELECT 1 FROM movie_upgrade_tracking_job_claim)
 OR EXISTS(SELECT 1 FROM job WHERE kind='movie_upgrade_tracking')
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.tracking-job-queue')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='upgrade.tracking-job-queue')
OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.tracking-job:%') THEN
  RAISE EXCEPTION 'upgrade tracking job history prevents downgrade'; END IF;
END $$;
DROP TRIGGER tracking_job_assert ON job;
DROP TRIGGER tracking_job_update ON job;
DROP TRIGGER tracking_claim_assert ON movie_upgrade_tracking_job_claim;
DROP TRIGGER tracking_claim_capture ON movie_upgrade_tracking_job_claim;
DROP TRIGGER tracking_claim_immutable ON movie_upgrade_tracking_job_claim;
DROP TRIGGER tracking_job_capture ON movie_upgrade_tracking_job;
DROP TRIGGER tracking_job_immutable ON movie_upgrade_tracking_job;
DROP FUNCTION pulse_upgrade_tracking_job_constraint();
DROP FUNCTION pulse_upgrade_tracking_job_update();
DO $$ DECLARE src TEXT; BEGIN
 src:=pg_get_functiondef('pulse_assert_upgrade_tracking_base72(uuid)'::regprocedure);
 EXECUTE replace(src,'pulse_assert_upgrade_tracking_base72','pulse_assert_upgrade_tracking');
END $$;
DROP FUNCTION pulse_assert_upgrade_tracking_base72(UUID);
DROP FUNCTION pulse_upgrade_tracking_claim_capture();
DROP FUNCTION pulse_assert_upgrade_tracking_job(UUID);
DROP FUNCTION pulse_upgrade_tracking_terminal_job(UUID);
DROP FUNCTION pulse_upgrade_tracking_job_capture();
DROP FUNCTION pulse_upgrade_tracking_job_running(job,movie_upgrade_tracking_job);
DROP TABLE movie_upgrade_tracking_job_claim;
DROP TABLE movie_upgrade_tracking_job;
-- +goose StatementEnd
