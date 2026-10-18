-- +goose Up
-- A new, explicit command is required after preparation. Existing receipts are not commands.
CREATE TABLE movie_upgrade_send_job (
 job_id UUID PRIMARY KEY REFERENCES job(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 command_id UUID NOT NULL UNIQUE CHECK(command_id<>'00000000-0000-0000-0000-000000000000'::uuid),
 operation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_preparation(operation_id) ON DELETE RESTRICT,
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL CHECK(length(command_key) BETWEEN 1 AND 100),
 send_key TEXT NOT NULL CHECK(send_key='job:'||command_id::text),
 prepared_sha256 BYTEA NOT NULL CHECK(octet_length(prepared_sha256)=32),
 original_sha256 BYTEA NOT NULL CHECK(octet_length(original_sha256)=32),
 protected_sha256 BYTEA NOT NULL CHECK(octet_length(protected_sha256)=32),
 commanded_at TIMESTAMPTZ NOT NULL,
 queued_job JSONB NOT NULL CHECK(jsonb_typeof(queued_job)='object')
);
CREATE TABLE movie_upgrade_send_job_claim (
 job_id UUID PRIMARY KEY REFERENCES movie_upgrade_send_job(job_id) ON DELETE RESTRICT,
 claimed_job JSONB NOT NULL CHECK(jsonb_typeof(claimed_job)='object'),
 session_nonce UUID NOT NULL,
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 backend_start TIMESTAMPTZ NOT NULL,
 captured_at TIMESTAMPTZ NOT NULL
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_send_job_original(op UUID) RETURNS BYTEA LANGUAGE SQL STABLE AS $$
 SELECT sha256(convert_to(jsonb_build_array(to_jsonb(a),to_jsonb(p),to_jsonb(t))::text,'UTF8'))
 FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id)
 JOIN download_task t ON t.id=p.task_id WHERE a.operation_id=op
$$;
CREATE FUNCTION pulse_upgrade_send_job_protected(op UUID) RETURNS BYTEA LANGUAGE SQL STABLE AS $$
 SELECT sha256(convert_to(jsonb_build_object(
 'root',(SELECT to_jsonb(r) FROM library_root r WHERE r.id=a.root_id),
 'client',(SELECT to_jsonb(c) FROM download_client c WHERE c.id=(p.task_snapshot->>'client_id')::uuid),
 'subscription',(SELECT to_jsonb(s) FROM subscription s WHERE s.id=a.subscription_id),
 'unit',(SELECT to_jsonb(u) FROM media_unit u WHERE u.id=a.unit_id),
 'file',(SELECT to_jsonb(f) FROM library_file f WHERE f.id=a.old_file_id),
 'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM unit_fact f WHERE f.unit_id=a.unit_id),
 'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.file_id,m.unit_id) FROM library_file_unit m WHERE m.file_id=a.old_file_id),
 'lane',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=a.operation_id),
 'reservation',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=a.operation_id),
 'requirement',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=a.operation_id)
 )::text,'UTF8')) FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id) WHERE a.operation_id=op
$$;
CREATE FUNCTION pulse_is_upgrade_send_job(j job) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE(EXISTS(SELECT 1 FROM movie_upgrade_send_job b JOIN movie_upgrade_preparation p USING(operation_id)
 WHERE b.job_id=(j).id AND b.task_id=p.task_id AND b.actor_id=p.actor_id
 AND b.prepared_sha256=sha256(convert_to(to_jsonb(p)::text,'UTF8'))
 AND b.original_sha256=pulse_upgrade_send_job_original(b.operation_id)
 AND (j).kind='movie_upgrade_native_send' AND (j).max_attempts=1
 AND (j).state IN ('queued','running','succeeded','dead','failed')
 AND (j).dedupe_key='movie-upgrade-send:'||b.operation_id::text
 AND (j).payload=jsonb_build_object('command_id',b.command_id,'operation_id',b.operation_id,'task_id',b.task_id,'actor_id',b.actor_id)),false)
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

 IF EXISTS(SELECT 1 FROM job j WHERE payload->>'operation_id'=op::text AND NOT (pulse_is_upgrade_observation_job(j) OR pulse_is_upgrade_send_job(j))) THEN
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
CREATE OR REPLACE FUNCTION pulse_upgrade_preparation_no_job() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id::text=NEW.payload->>'operation_id') THEN
  IF pulse_is_upgrade_send_job(NEW) THEN RETURN NEW; END IF;
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
CREATE FUNCTION pulse_upgrade_send_job_command_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE j job%ROWTYPE; p movie_upgrade_preparation%ROWTYPE;
BEGIN
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR UPDATE;
 SELECT * INTO STRICT p FROM movie_upgrade_preparation WHERE operation_id=NEW.operation_id FOR SHARE;
 IF NOT pulse_is_upgrade_send_job(j) OR to_jsonb(j) IS DISTINCT FROM NEW.queued_job
 OR j.state IS DISTINCT FROM 'queued' OR j.attempts IS DISTINCT FROM 0 OR j.lease_token IS DISTINCT FROM 0
 OR j.lease_owner IS NOT NULL OR j.lease_until IS NOT NULL OR j.result IS NOT NULL OR j.error IS NOT NULL OR j.finished_at IS NOT NULL
 OR NEW.commanded_at<p.prepared_at OR NEW.commanded_at>clock_timestamp()
 OR NEW.protected_sha256 IS DISTINCT FROM pulse_upgrade_send_job_protected(NEW.operation_id)
 OR EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=NEW.operation_id)
 OR EXISTS(SELECT 1 FROM movie_upgrade_observation WHERE operation_id=NEW.operation_id)
 THEN RAISE EXCEPTION 'invalid explicit upgrade SEND command' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_send_job_command'; END IF;
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NULL;
END $$;
CREATE FUNCTION pulse_upgrade_send_job_claim_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE j job%ROWTYPE; b movie_upgrade_send_job%ROWTYPE; expected JSONB;
BEGIN
 SELECT * INTO STRICT b FROM movie_upgrade_send_job WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR UPDATE;
 expected:=b.queued_job||jsonb_build_object('state','running','attempts',1,'lease_token',1,
 'lease_owner',j.lease_owner,'lease_until',j.lease_until,'updated_at',j.updated_at);
 IF to_jsonb(j) IS DISTINCT FROM NEW.claimed_job OR NEW.claimed_job IS DISTINCT FROM expected
 OR COALESCE(j.lease_owner,'')='' OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
 OR NEW.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_send_nonce',true)
 OR NEW.backend_pid IS DISTINCT FROM pg_backend_pid()
 OR NEW.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 OR NEW.captured_at>clock_timestamp() OR NEW.captured_at<b.commanded_at
 OR EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=b.operation_id)
 THEN RAISE EXCEPTION 'invalid original upgrade SEND claim' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_send_job_claim'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_upgrade_send_job_terminal(jid UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT c.claimed_job||jsonb_build_object('state','succeeded','lease_until',NULL,'updated_at',r.recorded_at,'finished_at',r.recorded_at,
 'result',jsonb_build_object('operation_id',b.operation_id,'task_id',b.task_id,'status',r.status,'execution_allowed',false))
 FROM movie_upgrade_send_job b JOIN movie_upgrade_send_job_claim c USING(job_id)
 JOIN movie_upgrade_send_result r USING(operation_id) WHERE b.job_id=jid AND r.request_key=b.send_key AND r.owner=b.job_id
$$;
CREATE FUNCTION pulse_assert_upgrade_send_job(jid UUID, terminal BOOLEAN) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_send_job%ROWTYPE; c movie_upgrade_send_job_claim%ROWTYPE; j job%ROWTYPE; expected JSONB; n TIMESTAMPTZ;
BEGIN
 SELECT * INTO STRICT b FROM movie_upgrade_send_job WHERE job_id=jid;
 SELECT * INTO STRICT c FROM movie_upgrade_send_job_claim WHERE job_id=jid;
 SELECT * INTO STRICT j FROM job WHERE id=jid FOR UPDATE;
 IF NOT pulse_is_upgrade_send_job(j) THEN RAISE EXCEPTION 'upgrade SEND binding changed'; END IF;
 IF terminal THEN expected:=pulse_upgrade_send_job_terminal(jid);
 ELSE
 expected:=c.claimed_job;
 IF NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=b.operation_id) AND b.protected_sha256 IS DISTINCT FROM pulse_upgrade_send_job_protected(b.operation_id)
 THEN RAISE EXCEPTION 'upgrade SEND protection changed'; END IF;
 END IF;
 IF expected IS NULL OR to_jsonb(j) IS DISTINCT FROM expected
 OR c.session_nonce::text IS DISTINCT FROM current_setting('pulsemedia.upgrade_send_nonce',true)
 OR c.backend_pid IS DISTINCT FROM pg_backend_pid()
 OR c.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 THEN RAISE EXCEPTION 'upgrade SEND original claim/session changed'; END IF;
 PERFORM pulse_assert_upgrade_preparation(b.operation_id);
 n:=clock_timestamp();
 IF (c.claimed_job->>'lease_until')::timestamptz IS NULL OR (c.claimed_job->>'lease_until')::timestamptz<=n
 THEN RAISE EXCEPTION 'upgrade SEND job lease expired' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_send_job_lease'; END IF;
END $$;
CREATE FUNCTION pulse_upgrade_send_job_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE b movie_upgrade_send_job%ROWTYPE; c movie_upgrade_send_job_claim%ROWTYPE; expected JSONB; terminal BOOLEAN;
BEGIN
 IF TG_TABLE_NAME='job' THEN SELECT * INTO b FROM movie_upgrade_send_job WHERE job_id=NEW.id;
 ELSE SELECT * INTO b FROM movie_upgrade_send_job WHERE operation_id=NEW.operation_id; END IF;
 IF NOT FOUND THEN
  IF TG_TABLE_NAME IN ('movie_upgrade_send_authorization','movie_upgrade_send_dispatch') THEN RAISE EXCEPTION 'explicit upgrade SEND command required' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_send_job_command'; END IF;
  RETURN NULL;
 END IF;
 SELECT * INTO c FROM movie_upgrade_send_job_claim WHERE job_id=b.job_id;
 SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=b.operation_id) INTO terminal;
 IF TG_TABLE_NAME='job' AND NOT terminal THEN
 IF OLD.state='queued' AND to_jsonb(OLD)=b.queued_job AND c.job_id IS NULL AND NEW.state='running' THEN
 expected:=b.queued_job||jsonb_build_object('state','running','attempts',1,'lease_token',1,
 'lease_owner',NEW.lease_owner,'lease_until',NEW.lease_until,'updated_at',NEW.updated_at);
 IF COALESCE(NEW.lease_owner,'')='' OR NEW.lease_until IS NULL OR NEW.lease_until<=clock_timestamp()
 THEN RAISE EXCEPTION 'upgrade SEND invalid claim'; END IF;
 ELSE
 IF NEW.state NOT IN ('dead','failed') OR OLD.state NOT IN ('queued','running')
 OR (c.job_id IS NOT NULL AND to_jsonb(OLD) IS DISTINCT FROM c.claimed_job)
 THEN RAISE EXCEPTION 'upgrade SEND job claim is frozen' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_send_job_frozen'; END IF;
 expected:=to_jsonb(OLD)||jsonb_build_object('state',NEW.state,'lease_owner',NULL,'lease_until',NULL,
 'error',NEW.error,'not_before',NEW.not_before,'updated_at',NEW.updated_at,'finished_at',NEW.finished_at);
 END IF;
 IF to_jsonb(NEW) IS DISTINCT FROM expected OR NEW.updated_at<OLD.updated_at
 OR (NEW.finished_at IS NOT NULL AND NEW.finished_at IS DISTINCT FROM NEW.updated_at)
 THEN RAISE EXCEPTION 'upgrade SEND job claim is frozen' USING ERRCODE='23514',CONSTRAINT='movie_upgrade_send_job_frozen'; END IF;
 RETURN NULL;
 END IF;
 IF TG_TABLE_NAME<>'job' THEN
  IF NEW.owner IS DISTINCT FROM b.job_id OR NEW.task_id IS DISTINCT FROM b.task_id OR NEW.actor_id IS DISTINCT FROM b.actor_id
  THEN RAISE EXCEPTION 'upgrade SEND return/permit binding mismatch'; END IF;
 END IF;
 PERFORM pulse_assert_upgrade_send_job(b.job_id,terminal);
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_send_job_command AFTER INSERT ON movie_upgrade_send_job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_job_command_guard();
CREATE TRIGGER movie_upgrade_send_job_claim_capture BEFORE INSERT ON movie_upgrade_send_job_claim FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_job_claim_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_send_job_terminal AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_job_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_send_job_arm AFTER INSERT ON movie_upgrade_send_authorization DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_job_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_send_job_dispatch AFTER INSERT ON movie_upgrade_send_dispatch DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_job_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_send_job_result AFTER INSERT ON movie_upgrade_send_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_send_job_guard();
CREATE TRIGGER movie_upgrade_send_job_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_send_job FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE TRIGGER movie_upgrade_send_job_claim_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_send_job_claim FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_send_job,movie_upgrade_send_job_claim FROM PUBLIC;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE job,movie_upgrade_send_job,movie_upgrade_send_job_claim,movie_upgrade_preparation,movie_upgrade_send_authorization,movie_upgrade_send_dispatch,movie_upgrade_send_result,audit_log IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM movie_upgrade_send_job) OR EXISTS(SELECT 1 FROM job WHERE kind='movie_upgrade_native_send')
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.queue-send')
 THEN RAISE EXCEPTION 'cannot downgrade explicit upgrade SEND history'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER movie_upgrade_send_job_terminal ON job;
DROP TRIGGER movie_upgrade_send_job_arm ON movie_upgrade_send_authorization;
DROP TRIGGER movie_upgrade_send_job_dispatch ON movie_upgrade_send_dispatch;
DROP TRIGGER movie_upgrade_send_job_result ON movie_upgrade_send_result;
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
DROP FUNCTION pulse_upgrade_send_job_guard();
DROP FUNCTION pulse_assert_upgrade_send_job(UUID,BOOLEAN);
DROP FUNCTION pulse_upgrade_send_job_terminal(UUID);
DROP TABLE movie_upgrade_send_job_claim;
DROP FUNCTION pulse_upgrade_send_job_claim_guard();
DROP TABLE movie_upgrade_send_job;
DROP FUNCTION pulse_upgrade_send_job_command_guard();
DROP FUNCTION pulse_is_upgrade_send_job(job);
DROP FUNCTION pulse_upgrade_send_job_original(UUID);
DROP FUNCTION pulse_upgrade_send_job_protected(UUID);
