-- +goose Up
CREATE TABLE movie_upgrade_observation_job (
 job_id UUID PRIMARY KEY REFERENCES job(id) ON DELETE RESTRICT,
 observation_id UUID NOT NULL UNIQUE,
 operation_id UUID NOT NULL UNIQUE,
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 admission_key TEXT NOT NULL CHECK(length(admission_key) BETWEEN 1 AND 130),
 result_key TEXT NOT NULL CHECK(length(result_key) BETWEEN 1 AND 130),
 claimed_job JSONB NOT NULL CHECK(jsonb_typeof(claimed_job)='object'),
 original_sha256 BYTEA NOT NULL CHECK(octet_length(original_sha256)=32),
 protected_sha256 BYTEA NOT NULL CHECK(octet_length(protected_sha256)=32),
 FOREIGN KEY(observation_id,operation_id) REFERENCES movie_upgrade_observation(observation_id,operation_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);
-- +goose StatementBegin
CREATE FUNCTION pulse_is_upgrade_observation_job(j job) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE((j).kind='movie_upgrade_observation' AND (j).max_attempts=1
 AND EXISTS(SELECT 1 FROM movie_upgrade_preparation p WHERE p.operation_id::text=(j).payload->>'operation_id'
  AND (j).payload=jsonb_build_object('operation_id',p.operation_id,'task_id',p.task_id,'actor_id',p.actor_id)
  AND (j).dedupe_key='movie-upgrade-observation:'||p.operation_id::text
  AND pulse_upgrade_observation_original_digest(p.operation_id) IS NOT NULL),false)
$$;
CREATE OR REPLACE FUNCTION pulse_assert_upgrade_preparation(op UUID)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
 a movie_upgrade_authority%ROWTYPE;
 l movie_upgrade_preparation_lease%ROWTYPE;
 p movie_upgrade_preparation%ROWTYPE;
 task JSONB;
 n BIGINT;
BEGIN
 SELECT * INTO a FROM movie_upgrade_authority WHERE operation_id=op;
 IF NOT FOUND THEN
  RETURN;
 END IF;

 IF a.goal<>'upgrade' OR a.status<>'reserved_not_executable' THEN
  RAISE EXCEPTION 'upgrade authority is not frozen';
 END IF;

 IF EXISTS(SELECT 1 FROM job j WHERE payload->>'operation_id'=op::text AND NOT pulse_is_upgrade_observation_job(j)) THEN
  RAISE EXCEPTION 'upgrade preparation cannot create jobs';
 END IF;

 SELECT count(*) INTO n FROM download_task WHERE operation_id=op;
 SELECT * INTO p FROM movie_upgrade_preparation WHERE operation_id=op;

 IF NOT FOUND THEN
  IF n<>0 OR EXISTS(
   SELECT 1 FROM movie_upgrade_preparation_lease
   WHERE operation_id=op AND state='prepared'
  ) THEN
   RAISE EXCEPTION 'upgrade physical task has no preparation';
  END IF;
  RETURN;
 END IF;

 SELECT * INTO l FROM movie_upgrade_preparation_lease WHERE operation_id=op;
 IF NOT FOUND OR l.state<>'prepared'
    OR l.actor_id<>a.actor_id OR p.actor_id<>a.actor_id
    OR l.owner<>p.owner OR l.token<>p.token
    OR p.prepared_at<l.claimed_at OR p.prepared_at>=l.lease_until
    OR p.evidence->'authority' IS DISTINCT FROM to_jsonb(a)
    OR p.evidence->>'raw_sha256' IS DISTINCT FROM encode(p.raw_sha256,'hex')
    OR p.evidence->>'protected_digest'
       IS DISTINCT FROM a.snapshot->>'protected_digest'
    OR p.evidence->>'root_generation'
       IS DISTINCT FROM a.root_generation::text THEN
  RAISE EXCEPTION 'upgrade preparation binding mismatch';
 END IF;

 SELECT to_jsonb(t) INTO task FROM download_task t WHERE id=p.task_id;
 IF n<>1 OR task IS NULL OR task IS DISTINCT FROM p.task_snapshot
    OR task->>'operation_id' IS DISTINCT FROM op::text
    OR task->>'state' IS DISTINCT FROM 'submitting'
    OR task->>'save_path' IS DISTINCT FROM p.evidence->>'save_path'
    OR task->>'client_id' IS DISTINCT FROM a.client_id::text
    OR task->>'candidate_id' IS DISTINCT FROM a.candidate_id::text
    OR task->>'info_hash' IS DISTINCT FROM p.evidence->'proof'->>'info_hash'
    OR task->>'total_bytes' IS DISTINCT FROM p.evidence->'proof'->>'total_bytes' THEN
  RAISE EXCEPTION 'upgrade preparation task mismatch';
 END IF;

 IF (SELECT count(*) FROM acquisition_reservation WHERE operation_id=op)<>1
    OR (SELECT count(*) FROM unit_reservation WHERE operation_id=op)<>1
    OR (SELECT count(*) FROM download_requirement WHERE operation_id=op)<>1
    OR NOT EXISTS(
     SELECT 1 FROM acquisition_reservation x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'lane'
      AND x.unit_id=a.unit_id AND x.source_root_id=a.root_id
      AND x.source_generation=a.root_generation
      AND x.candidate_id=a.candidate_id
      AND x.source_stamp=a.snapshot->>'source_stamp'
      AND x.acquisition_lane='default'
      AND x.task_id IS NULL AND NOT x.legacy_conflict
      AND x.released_at IS NULL
    )
    OR NOT EXISTS(
     SELECT 1 FROM unit_reservation x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'reservation'
      AND x.unit_id=a.unit_id AND x.root_id=a.root_id
      AND x.candidate_id=a.candidate_id
      AND x.purpose='upgrade' AND x.state='reserved'
      AND x.expires_at IS NULL
    )
    OR NOT EXISTS(
     SELECT 1 FROM download_requirement x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'requirement'
      AND x.unit_id=a.unit_id AND x.root_id=a.root_id
      AND x.actor_type='user' AND x.actor_id=a.actor_id::text
      AND x.state='pending' AND x.released_at IS NULL
      AND x.submission_fence=jsonb_build_object(
       'goal','upgrade','status','reserved_not_executable',
       'authority',a.operation_id,
       'protected_digest',a.snapshot->>'protected_digest',
       'observation',a.snapshot->'observation')
    ) THEN
  RAISE EXCEPTION 'upgrade preparation protection mismatch';
 END IF;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_observation_terminal_job(obs UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT b.claimed_job || jsonb_build_object('state','succeeded','result',jsonb_build_object(
 'observation_id',r.observation_id,'operation_id',b.operation_id,'task_id',b.task_id,
 'outcome',r.outcome,'disposition','manual_review','execution_allowed',false),
 'lease_until',NULL,'updated_at',r.recorded_at,'finished_at',r.recorded_at)
 FROM movie_upgrade_observation_job b JOIN movie_upgrade_observation_result r USING(observation_id)
 WHERE b.observation_id=obs AND r.request_key=b.result_key
$$;
CREATE FUNCTION pulse_upgrade_observation_job_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE actual JSONB;
BEGIN
 SELECT to_jsonb(j) INTO STRICT actual FROM job j WHERE id=NEW.job_id FOR UPDATE;
 IF actual IS DISTINCT FROM NEW.claimed_job OR actual->>'state' IS DISTINCT FROM 'running'
 OR (actual->>'lease_until')::timestamptz IS NULL OR (actual->>'lease_until')::timestamptz<=clock_timestamp()
 THEN RAISE EXCEPTION 'invalid original upgrade observation job claim' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_observation_job_claim'; END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_assert_upgrade_observation_job(obs UUID, terminal BOOLEAN) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_observation_job%ROWTYPE; o movie_upgrade_observation%ROWTYPE; j job%ROWTYPE; n TIMESTAMPTZ;
BEGIN
 SELECT * INTO STRICT b FROM movie_upgrade_observation_job WHERE observation_id=obs;
 SELECT * INTO STRICT o FROM movie_upgrade_observation WHERE observation_id=obs;
 SELECT * INTO STRICT j FROM job WHERE id=b.job_id FOR UPDATE;
 IF o.operation_id<>b.operation_id OR o.task_id<>b.task_id OR o.actor_id<>b.actor_id
 OR o.owner<>b.job_id OR o.request_key<>b.admission_key
 OR o.original_sha256<>b.original_sha256 OR o.protected_sha256<>b.protected_sha256
 OR (b.claimed_job->>'kind') IS DISTINCT FROM 'movie_upgrade_observation'
 OR (b.claimed_job->>'state') IS DISTINCT FROM 'running'
 OR (b.claimed_job->>'dedupe_key') IS DISTINCT FROM 'movie-upgrade-observation:'||b.operation_id::text
 OR b.claimed_job->'payload' IS DISTINCT FROM jsonb_build_object('operation_id',b.operation_id,'task_id',b.task_id,'actor_id',b.actor_id)
 OR b.claimed_job->'result' IS DISTINCT FROM 'null'::jsonb OR b.claimed_job->'error' IS DISTINCT FROM 'null'::jsonb
 OR (b.claimed_job->>'attempts')::int IS DISTINCT FROM 1 OR (b.claimed_job->>'max_attempts')::int IS DISTINCT FROM 1
 OR COALESCE(b.claimed_job->>'lease_owner','')=''
 OR b.claimed_job->>'lease_token' IS NULL OR (b.claimed_job->>'lease_token')::bigint<=0
 OR b.claimed_job IS DISTINCT FROM to_jsonb(jsonb_populate_record(NULL::job,b.claimed_job))
 THEN RAISE EXCEPTION 'invalid upgrade observation job binding'; END IF;
 IF terminal THEN
  IF pulse_upgrade_observation_terminal_job(obs) IS NULL OR to_jsonb(j) IS DISTINCT FROM pulse_upgrade_observation_terminal_job(obs)
  THEN RAISE EXCEPTION 'upgrade observation and job result disagree'; END IF;
 ELSE
  IF to_jsonb(j) IS DISTINCT FROM b.claimed_job THEN RAISE EXCEPTION 'upgrade observation job changed'; END IF;
 END IF;
 PERFORM pulse_assert_upgrade_observation(obs);
 -- Both deadlines are checked after every potentially blocking read/function.
 n:=clock_timestamp();
 IF (b.claimed_job->>'lease_until')::timestamptz IS NULL
 OR (b.claimed_job->>'lease_until')::timestamptz<=n OR o.lease_until<=n
 THEN RAISE EXCEPTION 'upgrade observation job lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_observation_job_lease'; END IF;
END;
$$;
CREATE FUNCTION pulse_upgrade_observation_job_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE obs UUID; completed BOOLEAN; claimed JSONB; expected JSONB;
BEGIN
 IF TG_TABLE_NAME='job' THEN
  SELECT observation_id INTO obs FROM movie_upgrade_observation_job WHERE job_id=NEW.id;
 ELSE obs:=NEW.observation_id; END IF;
 IF obs IS NULL OR NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_job WHERE observation_id=obs) THEN RETURN NULL; END IF;
 SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation_result WHERE observation_id=obs) INTO completed;
 -- Only explicit terminal failure may change an admitted claim without a result.
 -- Renewal, claim replacement, requeueing and later terminal edits are rejected.
 IF TG_TABLE_NAME='job' AND NOT completed THEN
  SELECT claimed_job INTO STRICT claimed FROM movie_upgrade_observation_job WHERE observation_id=obs;
  expected:=claimed||jsonb_build_object('state',NEW.state,'lease_owner',NULL,'lease_until',NULL,
   'error',NEW.error,'not_before',NEW.not_before,'updated_at',NEW.updated_at,'finished_at',NEW.finished_at);
  IF to_jsonb(OLD) IS DISTINCT FROM claimed OR NEW.state NOT IN ('dead','failed')
   OR NEW.lease_owner IS NOT NULL OR NEW.lease_until IS NOT NULL
   OR NEW.updated_at<OLD.updated_at
   OR (NEW.finished_at IS NOT NULL AND NEW.finished_at IS DISTINCT FROM NEW.updated_at)
   OR to_jsonb(NEW) IS DISTINCT FROM expected
  THEN RAISE EXCEPTION 'upgrade observation job claim is frozen' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_observation_job_frozen'; END IF;
  RETURN NULL;
 END IF;
 PERFORM pulse_assert_upgrade_observation_job(obs,completed);
 RETURN NULL;
END;
$$;
-- Preserve65's generic execution-job denial. Only exact read-only UNKNOWN observation jobs are admitted.
CREATE OR REPLACE FUNCTION pulse_upgrade_preparation_no_job() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id::text=NEW.payload->>'operation_id') THEN
  IF NEW.kind='movie_upgrade_observation' AND NEW.max_attempts=1
  AND EXISTS(SELECT 1 FROM movie_upgrade_preparation p WHERE p.operation_id::text=NEW.payload->>'operation_id'
   AND NEW.payload=jsonb_build_object('operation_id',p.operation_id,'task_id',p.task_id,'actor_id',p.actor_id)
   AND NEW.dedupe_key='movie-upgrade-observation:'||p.operation_id::text
   AND pulse_upgrade_observation_original_digest(p.operation_id) IS NOT NULL)
  THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'upgrade preparation cannot create jobs';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_observation_job_binding AFTER INSERT ON movie_upgrade_observation_job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_job_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_observation_job_result AFTER INSERT ON movie_upgrade_observation_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_job_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_observation_job_terminal AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_job_guard();
CREATE TRIGGER movie_upgrade_observation_job_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_observation_job FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER movie_upgrade_observation_job_capture BEFORE INSERT ON movie_upgrade_observation_job FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_observation_job_capture();
REVOKE ALL ON movie_upgrade_observation_job FROM PUBLIC;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE job,movie_upgrade_observation_job,movie_upgrade_observation,movie_upgrade_observation_permit,movie_upgrade_observation_result IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM movie_upgrade_observation_job) OR EXISTS(SELECT 1 FROM job WHERE kind='movie_upgrade_observation')
 THEN RAISE EXCEPTION 'cannot downgrade upgrade observation job history'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER movie_upgrade_observation_job_terminal ON job;
DROP TRIGGER movie_upgrade_observation_job_result ON movie_upgrade_observation_result;
DROP TABLE movie_upgrade_observation_job;
DROP FUNCTION pulse_upgrade_observation_job_guard();
DROP FUNCTION pulse_upgrade_observation_job_capture();
DROP FUNCTION pulse_assert_upgrade_observation_job(UUID,BOOLEAN);
DROP FUNCTION pulse_upgrade_observation_terminal_job(UUID);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_upgrade_preparation_no_job()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(
  SELECT 1 FROM movie_upgrade_authority
  WHERE operation_id::text=NEW.payload->>'operation_id'
 ) THEN
  RAISE EXCEPTION 'upgrade preparation cannot create jobs';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_assert_upgrade_preparation(op UUID)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
 a movie_upgrade_authority%ROWTYPE;
 l movie_upgrade_preparation_lease%ROWTYPE;
 p movie_upgrade_preparation%ROWTYPE;
 task JSONB;
 n BIGINT;
BEGIN
 SELECT * INTO a FROM movie_upgrade_authority WHERE operation_id=op;
 IF NOT FOUND THEN
  RETURN;
 END IF;

 IF a.goal<>'upgrade' OR a.status<>'reserved_not_executable' THEN
  RAISE EXCEPTION 'upgrade authority is not frozen';
 END IF;

 IF EXISTS(SELECT 1 FROM job WHERE payload->>'operation_id'=op::text) THEN
  RAISE EXCEPTION 'upgrade preparation cannot create jobs';
 END IF;

 SELECT count(*) INTO n FROM download_task WHERE operation_id=op;
 SELECT * INTO p FROM movie_upgrade_preparation WHERE operation_id=op;

 IF NOT FOUND THEN
  IF n<>0 OR EXISTS(
   SELECT 1 FROM movie_upgrade_preparation_lease
   WHERE operation_id=op AND state='prepared'
  ) THEN
   RAISE EXCEPTION 'upgrade physical task has no preparation';
  END IF;
  RETURN;
 END IF;

 SELECT * INTO l FROM movie_upgrade_preparation_lease WHERE operation_id=op;
 IF NOT FOUND OR l.state<>'prepared'
    OR l.actor_id<>a.actor_id OR p.actor_id<>a.actor_id
    OR l.owner<>p.owner OR l.token<>p.token
    OR p.prepared_at<l.claimed_at OR p.prepared_at>=l.lease_until
    OR p.evidence->'authority' IS DISTINCT FROM to_jsonb(a)
    OR p.evidence->>'raw_sha256' IS DISTINCT FROM encode(p.raw_sha256,'hex')
    OR p.evidence->>'protected_digest'
       IS DISTINCT FROM a.snapshot->>'protected_digest'
    OR p.evidence->>'root_generation'
       IS DISTINCT FROM a.root_generation::text THEN
  RAISE EXCEPTION 'upgrade preparation binding mismatch';
 END IF;

 SELECT to_jsonb(t) INTO task FROM download_task t WHERE id=p.task_id;
 IF n<>1 OR task IS NULL OR task IS DISTINCT FROM p.task_snapshot
    OR task->>'operation_id' IS DISTINCT FROM op::text
    OR task->>'state' IS DISTINCT FROM 'submitting'
    OR task->>'save_path' IS DISTINCT FROM p.evidence->>'save_path'
    OR task->>'client_id' IS DISTINCT FROM a.client_id::text
    OR task->>'candidate_id' IS DISTINCT FROM a.candidate_id::text
    OR task->>'info_hash' IS DISTINCT FROM p.evidence->'proof'->>'info_hash'
    OR task->>'total_bytes' IS DISTINCT FROM p.evidence->'proof'->>'total_bytes' THEN
  RAISE EXCEPTION 'upgrade preparation task mismatch';
 END IF;

 IF (SELECT count(*) FROM acquisition_reservation WHERE operation_id=op)<>1
    OR (SELECT count(*) FROM unit_reservation WHERE operation_id=op)<>1
    OR (SELECT count(*) FROM download_requirement WHERE operation_id=op)<>1
    OR NOT EXISTS(
     SELECT 1 FROM acquisition_reservation x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'lane'
      AND x.unit_id=a.unit_id AND x.source_root_id=a.root_id
      AND x.source_generation=a.root_generation
      AND x.candidate_id=a.candidate_id
      AND x.source_stamp=a.snapshot->>'source_stamp'
      AND x.acquisition_lane='default'
      AND x.task_id IS NULL AND NOT x.legacy_conflict
      AND x.released_at IS NULL
    )
    OR NOT EXISTS(
     SELECT 1 FROM unit_reservation x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'reservation'
      AND x.unit_id=a.unit_id AND x.root_id=a.root_id
      AND x.candidate_id=a.candidate_id
      AND x.purpose='upgrade' AND x.state='reserved'
      AND x.expires_at IS NULL
    )
    OR NOT EXISTS(
     SELECT 1 FROM download_requirement x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'requirement'
      AND x.unit_id=a.unit_id AND x.root_id=a.root_id
      AND x.actor_type='user' AND x.actor_id=a.actor_id::text
      AND x.state='pending' AND x.released_at IS NULL
      AND x.submission_fence=jsonb_build_object(
       'goal','upgrade','status','reserved_not_executable',
       'authority',a.operation_id,
       'protected_digest',a.snapshot->>'protected_digest',
       'observation',a.snapshot->'observation')
    ) THEN
  RAISE EXCEPTION 'upgrade preparation protection mismatch';
 END IF;
END $$;
-- +goose StatementEnd
DROP FUNCTION pulse_is_upgrade_observation_job(job);
