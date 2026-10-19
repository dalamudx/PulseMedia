-- +goose Up
CREATE TABLE automatic_tv_observation_job (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 operation_id UUID NOT NULL REFERENCES automatic_tv_submission_preparation(operation_id),
 task_id UUID NOT NULL REFERENCES download_task(id),
 send_attempt_id UUID NOT NULL REFERENCES automatic_tv_physical_attempt(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX automatic_tv_observation_job_operation ON automatic_tv_observation_job(operation_id,created_at,job_id);
CREATE TABLE automatic_tv_observation_permit (
 id UUID PRIMARY KEY,
 operation_id UUID NOT NULL REFERENCES automatic_tv_execution_authority(operation_id),
 task_id UUID NOT NULL REFERENCES download_task(id),
 job_id UUID NOT NULL REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 send_attempt_id UUID NOT NULL REFERENCES automatic_tv_physical_attempt(id),
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 advisory_key BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX automatic_tv_observation_permit_active ON automatic_tv_observation_permit(operation_id) WHERE finished_at IS NULL;
CREATE TABLE automatic_tv_observation_result (
 job_id UUID PRIMARY KEY REFERENCES automatic_tv_observation_job(job_id),
 operation_id UUID NOT NULL REFERENCES automatic_tv_submission_preparation(operation_id),
 task_id UUID NOT NULL REFERENCES download_task(id),
 permit_id UUID NOT NULL UNIQUE REFERENCES automatic_tv_observation_permit(id),
 send_attempt_id UUID NOT NULL REFERENCES automatic_tv_physical_attempt(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 authority_hash TEXT NOT NULL CHECK(authority_hash ~ '^[0-9a-f]{64}$'),
 proof_hash TEXT NOT NULL CHECK(proof_hash ~ '^[0-9a-f]{64}$'),
 state TEXT NOT NULL CHECK(state IN ('tracking','unknown','completed')),
 complete BOOLEAN NOT NULL,
 review_required BOOLEAN NOT NULL,
 evidence_stamp TEXT NOT NULL CHECK(evidence_stamp ~ '^[0-9a-f]{64}$'),
 -- Preserve the canonical typed bytes for the statement hash. JSONB's textual
 -- rendering changes whitespace and cannot be substituted for these bytes.
 statement TEXT NOT NULL CHECK(octet_length(statement)<=4194304),
 inventory JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 next_poll_at TIMESTAMPTZ,
 CHECK(evidence_stamp=encode(sha256(convert_to(statement,'UTF8')),'hex')),
 CHECK(COALESCE(statement::jsonb->>'kind'='automatic_tv_observation_v1'
  AND (statement::jsonb->>'complete')::boolean=complete
  AND (statement::jsonb->>'review_required')::boolean=review_required
  AND statement::jsonb->'Files'=inventory,false)),
 CHECK((state='completed' AND complete AND next_poll_at IS NULL AND jsonb_typeof(inventory)='array' AND jsonb_array_length(inventory) BETWEEN 1 AND 4096)
  OR (state='tracking' AND NOT complete AND NOT review_required AND inventory='null'::jsonb AND next_poll_at>=created_at+interval '2 seconds')
  OR (state='unknown' AND NOT complete AND review_required AND inventory='null'::jsonb AND next_poll_at IS NULL))
);
CREATE INDEX automatic_tv_observation_poll ON automatic_tv_observation_result(operation_id,next_poll_at) WHERE next_poll_at IS NOT NULL;
CREATE UNIQUE INDEX automatic_tv_organization_once ON job((payload->>'task_id')) WHERE kind='download_organization' AND payload ? 'automatic_tv_authority';

CREATE TRIGGER automatic_tv_observation_job_history BEFORE UPDATE OR DELETE ON automatic_tv_observation_job FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_observation_job_truncate BEFORE TRUNCATE ON automatic_tv_observation_job FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_observation_result_history BEFORE UPDATE OR DELETE ON automatic_tv_observation_result FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_observation_result_truncate BEFORE TRUNCATE ON automatic_tv_observation_result FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_observation_permit_history BEFORE UPDATE OR DELETE ON automatic_tv_observation_permit FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_physical_attempt();
CREATE TRIGGER automatic_tv_observation_permit_truncate BEFORE TRUNCATE ON automatic_tv_observation_permit FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_physical_attempt();

-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_observation_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p automatic_tv_submission_preparation; j job; x automatic_tv_physical_attempt; b automatic_tv_observation_job;
 k BIGINT; permitted BOOLEAN;
BEGIN
 SELECT * INTO p FROM automatic_tv_submission_preparation WHERE operation_id=NEW.operation_id;
 SELECT * INTO j FROM job WHERE id=NEW.job_id FOR UPDATE;
 SELECT * INTO x FROM automatic_tv_physical_attempt WHERE id=NEW.send_attempt_id;
 IF p.operation_id IS NULL OR j.id IS NULL OR x.id IS NULL OR p.task_id<>NEW.task_id OR p.physical_attempt_id<>NEW.send_attempt_id
  OR x.operation_id<>NEW.operation_id OR x.job_id<>p.job_id OR x.lease_owner<>p.lease_owner OR x.lease_token<>p.lease_token OR x.finished_at IS NULL
  OR EXISTS(SELECT 1 FROM automatic_tv_physical_attempt WHERE operation_id=NEW.operation_id AND finished_at IS NULL)
  OR j.kind<>'automatic_tv_observation' OR j.dedupe_key IS DISTINCT FROM NEW.operation_id::text
  OR j.payload IS DISTINCT FROM jsonb_build_object('operation_id',NEW.operation_id,'task_id',NEW.task_id,'automatic_tv_authority',NEW.operation_id)
 THEN RAISE EXCEPTION 'automatic TV observation requires original finished preparation and exact job'; END IF;
 IF TG_TABLE_NAME='automatic_tv_observation_job' THEN
  IF j.state<>'queued' OR j.lease_owner IS NOT NULL OR j.lease_until IS NOT NULL OR j.attempts<>0 OR j.lease_token<>0
   OR EXISTS(SELECT 1 FROM automatic_tv_observation_permit WHERE operation_id=NEW.operation_id AND finished_at IS NULL)
   OR EXISTS(SELECT 1 FROM automatic_tv_observation_result WHERE operation_id=NEW.operation_id AND (complete OR review_required))
  THEN RAISE EXCEPTION 'automatic TV observation cannot resurrect reviewed work'; END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO b FROM automatic_tv_observation_job WHERE job_id=NEW.job_id;
 IF b.job_id IS NULL OR b.operation_id<>NEW.operation_id OR b.task_id<>NEW.task_id OR b.send_attempt_id<>NEW.send_attempt_id
  OR j.state<>'running' OR j.lease_owner IS DISTINCT FROM NEW.lease_owner OR j.lease_token<>NEW.lease_token OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
 THEN RAISE EXCEPTION 'automatic TV observation requires fresh independent lease'; END IF;
 k:=('x'||substr(encode(sha256(convert_to('pulsemedia.automatic-tv.physical.v1:'||NEW.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint;
 IF TG_TABLE_NAME='automatic_tv_observation_permit' THEN
  IF NEW.finished_at IS NOT NULL OR NEW.advisory_key<>k OR NOT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=NEW.backend_pid AND granted AND mode='ExclusiveLock' AND objsubid=1
   AND classid=(k>>32 & 4294967295)::oid AND objid=(k & 4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))
  THEN RAISE EXCEPTION 'automatic TV observation requires held physical session'; END IF;
 ELSE
  SELECT EXISTS(SELECT 1 FROM automatic_tv_observation_permit gate WHERE gate.id=NEW.permit_id AND gate.operation_id=NEW.operation_id AND gate.task_id=NEW.task_id
   AND gate.job_id=NEW.job_id AND gate.lease_owner=NEW.lease_owner AND gate.lease_token=NEW.lease_token AND gate.send_attempt_id=NEW.send_attempt_id AND gate.finished_at IS NULL AND gate.advisory_key=k
   AND EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=gate.backend_pid AND granted AND mode='ExclusiveLock' AND objsubid=1
    AND classid=(k>>32 & 4294967295)::oid AND objid=(k & 4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))) INTO permitted;
  IF NOT permitted OR NEW.authority_hash<>p.authority_hash OR NEW.proof_hash<>p.proof_hash
   OR NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority a WHERE a.operation_id=NEW.operation_id AND a.client_id::text=NEW.statement::jsonb->>'client_id'
    AND a.snapshot->'target'->>'client_stamp'=NEW.statement::jsonb->>'client_stamp')
  THEN RAISE EXCEPTION 'automatic TV observation result requires original proof and held permit'; END IF;
  IF NEW.complete THEN
   IF NOT COALESCE(jsonb_typeof(NEW.statement::jsonb->'Before')='array' AND jsonb_array_length(NEW.statement::jsonb->'Before')=1
     AND jsonb_typeof(NEW.statement::jsonb->'After')='array' AND jsonb_array_length(NEW.statement::jsonb->'After')=1
     AND NEW.statement::jsonb->'Before'->0->>'ContentPath' IS NOT DISTINCT FROM NEW.statement::jsonb->'After'->0->>'ContentPath',false)
    OR EXISTS(SELECT 1 FROM jsonb_array_elements((NEW.statement::jsonb->'Before')||(NEW.statement::jsonb->'After')) native WHERE NOT COALESCE(
     native ?& ARRAY['InfoHash','SavePath','ContentPath','State','Tags','TotalBytes','VerifiedBytes','Complete']
     AND native->>'InfoHash'=p.info_hash AND native->>'SavePath'=p.proof->>'save_path'
     AND jsonb_typeof(native->'Complete')='boolean' AND (native->>'Complete')::boolean
     AND (native->>'TotalBytes')::numeric=(p.proof->>'total_bytes')::numeric AND (native->>'VerifiedBytes')::numeric=(p.proof->>'total_bytes')::numeric
     AND native->>'State' IN ('uploading','seeding','stopped','stalledUP','pausedUP','stoppedUP','queuedUP','forcedUP')
     AND jsonb_typeof(native->'Tags')='array' AND native->'Tags' ? ('pulsemedia:op:'||NEW.operation_id::text),false))
   THEN RAISE EXCEPTION 'automatic TV completion requires stable complete native integer observations'; END IF;
   IF (SELECT count(DISTINCT (f->>'Index')::integer) FROM jsonb_array_elements(NEW.inventory) f)<>jsonb_array_length(NEW.inventory)
    OR EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.inventory) f WHERE NOT f ?& ARRAY['Index','Path','Size','CompletedBytes','Wanted'] OR jsonb_typeof(f->'Wanted')<>'boolean' OR (f->>'Index')::integer<0 OR (f->>'Index')::integer>=jsonb_array_length(NEW.inventory)
      OR (f->>'Size')::numeric<0 OR (f->>'Size')::numeric<>(f->>'CompletedBytes')::numeric)
    OR (SELECT sum((f->>'Size')::numeric) FROM jsonb_array_elements(NEW.inventory) f)<>(p.proof->>'total_bytes')::numeric
    OR (SELECT jsonb_agg(jsonb_build_object('Path',f->>'Path','Size',(f->>'Size')::bigint) ORDER BY f->>'Path') FROM jsonb_array_elements(NEW.inventory) f)
     IS DISTINCT FROM (SELECT jsonb_agg(f ORDER BY f->>'Path') FROM jsonb_array_elements(p.proof->'files') f)
   THEN RAISE EXCEPTION 'automatic TV completion requires exact full original inventory'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_observation_job_binding BEFORE INSERT ON automatic_tv_observation_job FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_observation_binding();
CREATE TRIGGER automatic_tv_observation_permit_binding BEFORE INSERT ON automatic_tv_observation_permit FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_observation_binding();
CREATE TRIGGER automatic_tv_observation_result_binding BEFORE INSERT ON automatic_tv_observation_result FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_observation_binding();

-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_observation_send_exclusion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_submission_preparation WHERE operation_id=NEW.operation_id)
  OR EXISTS(SELECT 1 FROM automatic_tv_observation_permit WHERE operation_id=NEW.operation_id AND finished_at IS NULL)
 THEN RAISE EXCEPTION 'original automatic TV send cannot replay after preparation or during observation'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_observation_send_exclusion BEFORE INSERT ON automatic_tv_physical_attempt FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_observation_send_exclusion();

-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_observation_publication() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE t download_task; p automatic_tv_submission_preparation; a automatic_tv_execution_authority; r automatic_tv_observation_result; j job;
BEGIN
 IF TG_TABLE_NAME='job' THEN
  IF NEW.kind<>'download_organization' THEN RETURN NULL; END IF;
  SELECT * INTO t FROM download_task WHERE id::text=NEW.payload->>'task_id';
  IF NOT NEW.payload ? 'automatic_tv_authority' AND NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=t.operation_id) THEN RETURN NULL; END IF;
  SELECT * INTO r FROM automatic_tv_observation_result WHERE job_id=(NEW.payload->>'observation_job_id')::uuid;
  IF r.job_id IS NULL OR NOT r.complete OR r.review_required OR NEW.dedupe_key IS DISTINCT FROM 'organize:'||r.task_id::text
   OR NEW.payload IS DISTINCT FROM jsonb_build_object('operation_id',r.operation_id,'task_id',r.task_id,'automatic_tv_authority',r.operation_id,'observation_job_id',r.job_id,'inventory_stamp',r.evidence_stamp)
  THEN RAISE EXCEPTION 'automatic TV organization requires complete eligible original inventory'; END IF;
  RETURN NULL;
 END IF;
 IF TG_TABLE_NAME='download_task' THEN SELECT * INTO t FROM download_task WHERE id=NEW.id;
 ELSE SELECT * INTO t FROM download_task WHERE id=NEW.task_id; END IF;
 SELECT * INTO a FROM automatic_tv_execution_authority WHERE operation_id=t.operation_id;
 IF a.operation_id IS NULL THEN RETURN NULL; END IF;
 SELECT * INTO p FROM automatic_tv_submission_preparation WHERE operation_id=t.operation_id;
 IF p.operation_id IS NULL OR t.id<>p.task_id OR t.client_id<>a.client_id OR t.candidate_id<>a.candidate_id OR t.info_hash<>p.info_hash OR t.save_path<>p.proof->>'save_path'
 THEN RAISE EXCEPTION 'automatic TV task requires exact original preparation'; END IF;
 IF TG_TABLE_NAME='automatic_tv_observation_result' THEN
  SELECT * INTO j FROM job WHERE id=NEW.job_id FOR UPDATE;
  IF j.state<>'succeeded' OR j.lease_token<>NEW.lease_token OR j.lease_owner IS NOT NULL OR j.lease_until IS NOT NULL
   OR j.result IS DISTINCT FROM jsonb_build_object('task_id',NEW.task_id,'state',NEW.state,'review_required',NEW.review_required,'complete',NEW.complete,'evidence_stamp',NEW.evidence_stamp)
  THEN RAISE EXCEPTION 'automatic TV result requires completed original observation job'; END IF;
 END IF;
 IF t.state='tracking' AND NOT EXISTS(SELECT 1 FROM automatic_tv_submission_result WHERE operation_id=t.operation_id AND state='tracking')
  AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result WHERE operation_id=t.operation_id AND state='tracking' AND NOT review_required)
 THEN RAISE EXCEPTION 'automatic TV tracking requires original result'; END IF;
 IF t.state='completed' THEN
  SELECT * INTO r FROM automatic_tv_observation_result WHERE task_id=t.id AND complete;
  IF r.job_id IS NULL OR t.total_bytes<>(p.proof->>'total_bytes')::bigint OR t.downloaded_bytes<>t.total_bytes OR t.progress<>1
   OR t.content_path IS DISTINCT FROM NULLIF(r.statement::jsonb->'After'->0->>'ContentPath','')
   OR (r.review_required AND t.organization_state<>'review_required')
   OR NOT EXISTS(SELECT 1 FROM job WHERE id=r.job_id AND state='succeeded' AND lease_token=r.lease_token)
  THEN RAISE EXCEPTION 'automatic TV completion requires sealed complete inventory'; END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER automatic_tv_observation_task_publication AFTER INSERT OR UPDATE ON download_task DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_observation_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_observation_result_publication AFTER INSERT ON automatic_tv_observation_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_observation_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_observation_organization_publication AFTER INSERT OR UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_observation_publication();

-- +goose Down
LOCK TABLE automatic_tv_observation_permit,automatic_tv_observation_job,automatic_tv_observation_result,automatic_tv_physical_attempt,job,download_task,audit_log,outbox_event IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_observation_job) OR EXISTS(SELECT 1 FROM automatic_tv_observation_permit) OR EXISTS(SELECT 1 FROM automatic_tv_observation_result)
  OR EXISTS(SELECT 1 FROM job WHERE kind='automatic_tv_observation' OR payload ? 'observation_job_id')
  OR EXISTS(SELECT 1 FROM audit_log WHERE action='automatic_tv.observation_recorded') OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='automatic_tv.observation_recorded')
 THEN RAISE EXCEPTION 'automatic TV observation history downgrade refused'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER automatic_tv_observation_task_publication ON download_task;
DROP TRIGGER automatic_tv_observation_organization_publication ON job;
DROP TRIGGER automatic_tv_observation_send_exclusion ON automatic_tv_physical_attempt;
DROP TABLE automatic_tv_observation_result,automatic_tv_observation_permit,automatic_tv_observation_job;
DROP INDEX automatic_tv_organization_once;
DROP FUNCTION check_automatic_tv_observation_publication();
DROP FUNCTION guard_automatic_tv_observation_send_exclusion();
DROP FUNCTION check_automatic_tv_observation_binding();
