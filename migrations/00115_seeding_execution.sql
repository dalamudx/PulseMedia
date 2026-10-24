-- +goose Up
-- A lost database session is not proof that external I/O has stopped.
CREATE TABLE seeding_removal_execution (
 job_id UUID NOT NULL REFERENCES seeding_removal_dispatch(job_id),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 lease_owner TEXT NOT NULL CHECK(length(lease_owner)>0),
 client_id UUID NOT NULL REFERENCES download_client(id),
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 observe_only BOOLEAN NOT NULL,
 requests INTEGER NOT NULL DEFAULT 0 CHECK(requests BETWEEN 0 AND 3),
 opened_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 started_at TIMESTAMPTZ,
 result TEXT CHECK(result IN ('ineligible','absent','present','acknowledged','unknown','failed')),
 finished_at TIMESTAMPTZ,
 released_at TIMESTAMPTZ,
 PRIMARY KEY(job_id,lease_token),
 CHECK((requests=0)=(started_at IS NULL)),
 CHECK((result IS NULL)=(finished_at IS NULL)),
 CHECK(started_at IS NULL OR started_at>=opened_at),
 CHECK(finished_at IS NULL OR finished_at>=opened_at),
 CHECK(released_at IS NULL OR released_at>=opened_at),
 CHECK(NOT observe_only OR requests<=1)
);
CREATE UNIQUE INDEX seeding_execution_physical_client ON seeding_removal_execution(client_id) WHERE released_at IS NULL;
-- +goose StatementBegin
CREATE FUNCTION pulse_seeding_execution_gate_held(client UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted
 AND pid=pg_backend_pid() AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
 AND classid=((hashtextextended('seeding-removal:'||client::text,0)>>32)&4294967295)::oid
 AND objid=(hashtextextended('seeding-removal:'||client::text,0)&4294967295)::oid);
$$;
CREATE FUNCTION pulse_seeding_execution_history() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'seeding execution history protected' USING ERRCODE='23514'; END IF;
 IF (NEW.job_id,NEW.lease_token,NEW.lease_owner,NEW.client_id,NEW.backend_pid,NEW.observe_only,NEW.opened_at) IS DISTINCT FROM
 (OLD.job_id,OLD.lease_token,OLD.lease_owner,OLD.client_id,OLD.backend_pid,OLD.observe_only,OLD.opened_at)
 OR (OLD.released_at IS NOT NULL AND NEW IS DISTINCT FROM OLD)
 OR NEW.requests<OLD.requests OR NEW.requests>OLD.requests+1
 OR (OLD.started_at IS NOT NULL AND NEW.started_at IS DISTINCT FROM OLD.started_at)
 OR (OLD.result IS NOT NULL AND (NEW.result,NEW.finished_at,NEW.requests) IS DISTINCT FROM (OLD.result,OLD.finished_at,OLD.requests)) THEN
  RAISE EXCEPTION 'seeding execution identity or physical evidence immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_seeding_execution_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE e seeding_removal_execution%ROWTYPE; d seeding_removal_dispatch%ROWTYPE; j job%ROWTYPE;
 s seeding_removal_schedule%ROWTYPE; c seeding_configuration%ROWTYPE; p seeding_removal_permit%ROWTYPE;
 expected_job TEXT; expected_schedule TEXT;
BEGIN
 SELECT * INTO STRICT e FROM seeding_removal_execution WHERE job_id=NEW.job_id AND lease_token=NEW.lease_token;
 IF e.backend_pid<>pg_backend_pid() OR NOT pulse_seeding_execution_gate_held(e.client_id) THEN
  RAISE EXCEPTION 'seeding physical ownership unavailable' USING ERRCODE='23514';
 END IF;
 -- Release is after joined physical I/O; an expired logical lease must not stop
 -- its original owner from recording this fact. It cannot rebind or erase history.
 IF TG_OP='UPDATE' AND OLD.released_at IS NULL AND NEW.released_at IS NOT NULL THEN
  IF to_jsonb(NEW)-'released_at' IS DISTINCT FROM to_jsonb(OLD)-'released_at' OR NEW.released_at>clock_timestamp() THEN
   RAISE EXCEPTION 'seeding release changed execution facts' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
 END IF;
 IF e.released_at IS NOT NULL OR NOT EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND NOT isolated) THEN
  RAISE EXCEPTION 'seeding execution isolated or released' USING ERRCODE='23514';
 END IF;
 SELECT * INTO STRICT d FROM seeding_removal_dispatch WHERE job_id=e.job_id;
 PERFORM 1 FROM app_user WHERE id=d.owner_id FOR SHARE;
 PERFORM 1 FROM download_client WHERE id=d.client_id FOR SHARE;
 SELECT * INTO STRICT c FROM seeding_configuration WHERE client_id=d.client_id FOR SHARE;
 SELECT * INTO STRICT j FROM job WHERE id=e.job_id;
 SELECT * INTO STRICT s FROM seeding_removal_schedule WHERE task_id=d.task_id;
 SELECT * INTO p FROM seeding_removal_permit WHERE task_id=d.task_id;
 IF d.client_id<>e.client_id OR c.owner_id<>d.owner_id OR c.client_stamp<>d.client_stamp
 OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=d.owner_id AND is_active AND role='admin')
 OR pulse_global_rates_binding(d.client_id) IS DISTINCT FROM d.client_stamp
 OR j.kind<>'seeding_removal' OR j.max_attempts<>3
 OR j.payload IS DISTINCT FROM jsonb_build_object('task_id',d.task_id,'client_id',d.client_id,'config_version',d.config_version)
 OR j.lease_owner IS DISTINCT FROM e.lease_owner OR j.lease_token<>e.lease_token OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
 OR s.config_version<>d.config_version THEN
  RAISE EXCEPTION 'seeding execution authority or lease changed' USING ERRCODE='23514';
 END IF;
 IF e.observe_only THEN
  IF p.job_id IS DISTINCT FROM e.job_id OR p.client_stamp IS DISTINCT FROM d.client_stamp THEN RAISE EXCEPTION 'seeding observation lacks original permit' USING ERRCODE='23514'; END IF;
 ELSE
  IF NOT c.enabled OR c.version<>d.config_version OR (p.task_id IS NOT NULL AND (p.job_id,p.lease_owner,p.lease_token) IS DISTINCT FROM (e.job_id,e.lease_owner,e.lease_token)) THEN
   RAISE EXCEPTION 'seeding write execution cannot reuse permanent authority' USING ERRCODE='23514';
  END IF;
 END IF;
 IF e.result IS NULL THEN
  IF j.state<>'running' OR s.active_job_id IS DISTINCT FROM j.id OR s.state<>'running' THEN RAISE EXCEPTION 'seeding execution task graph changed' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 IF (e.result IN ('acknowledged','unknown','present') AND p.job_id IS DISTINCT FROM e.job_id)
 OR (e.result IN ('ineligible','failed') AND p.task_id IS NOT NULL)
 OR (e.result='acknowledged' AND e.observe_only) OR (e.result='present' AND NOT e.observe_only) THEN
  RAISE EXCEPTION 'seeding execution outcome lacks authority evidence' USING ERRCODE='23514';
 END IF;
 expected_job:=CASE WHEN e.result='unknown' AND j.attempts<j.max_attempts THEN 'queued' WHEN e.result IN ('failed','present','unknown') THEN 'dead' ELSE 'succeeded' END;
 expected_schedule:=CASE WHEN e.result='unknown' AND j.attempts<j.max_attempts THEN 'queued' WHEN e.result IN ('present','unknown') THEN 'review_required' WHEN e.result IN ('absent','acknowledged') THEN 'retired' ELSE 'idle' END;
 IF j.state<>expected_job OR s.state<>expected_schedule
 OR (expected_schedule='queued' AND s.active_job_id IS DISTINCT FROM j.id)
 OR (expected_schedule<>'queued' AND s.active_job_id IS NOT NULL)
 OR j.result IS DISTINCT FROM jsonb_build_object('task_id',d.task_id,'outcome',e.result,'observe_only',e.observe_only)
 OR NOT EXISTS(SELECT 1 FROM audit_log WHERE action='seeding.removal.finished' AND target_type='download_task' AND target_id=d.task_id::text AND payload->>'job_id'=j.id::text AND payload->>'lease_token'=e.lease_token::text)
 OR NOT EXISTS(SELECT 1 FROM outbox_event WHERE aggregate_type='seeding.removal' AND aggregate_id=d.task_id::text AND event_type='seeding.removal.finished' AND payload->>'job_id'=j.id::text AND payload->>'lease_token'=e.lease_token::text) THEN
  RAISE EXCEPTION 'seeding execution final graph or success ledger changed' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER seeding_execution_history BEFORE UPDATE OR DELETE ON seeding_removal_execution FOR EACH ROW EXECUTE FUNCTION pulse_seeding_execution_history();
CREATE TRIGGER seeding_execution_truncate BEFORE TRUNCATE ON seeding_removal_execution FOR EACH STATEMENT EXECUTE FUNCTION pulse_seeding_execution_history();
CREATE CONSTRAINT TRIGGER seeding_execution_commit AFTER INSERT OR UPDATE ON seeding_removal_execution DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_seeding_execution_commit();
REVOKE ALL ON seeding_removal_execution FROM PUBLIC;
-- +goose Down
LOCK TABLE seeding_removal_execution IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM seeding_removal_execution) THEN RAISE EXCEPTION 'seeding execution history prevents downgrade'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE seeding_removal_execution;
DROP FUNCTION pulse_seeding_execution_commit();
DROP FUNCTION pulse_seeding_execution_history();
DROP FUNCTION pulse_seeding_execution_gate_held(UUID);
