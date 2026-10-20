-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION pulse_checkin_calendar(instant timestamptz, zone text, daily text)
RETURNS TABLE(calendar_day date, day_start timestamptz, day_end timestamptz, due boolean)
LANGUAGE sql STABLE STRICT AS $$
 SELECT (instant AT TIME ZONE zone)::date,
 (instant AT TIME ZONE zone)::date::timestamp AT TIME ZONE zone,
 ((instant AT TIME ZONE zone)::date+1)::timestamp AT TIME ZONE zone,
 (instant AT TIME ZONE zone)::time >= daily::time
 WHERE pulse_checkin_timezone_known(zone)
$$;
-- +goose StatementEnd
CREATE TABLE site_checkin_day (
 id uuid PRIMARY KEY,
 indexer_id uuid NOT NULL REFERENCES indexer(id),
 expected_account_id text NOT NULL,
 profile text NOT NULL,
 timezone text NOT NULL CHECK(pulse_checkin_timezone_known(timezone)),
 calendar_day date NOT NULL,
 day_start timestamptz NOT NULL,
 day_end timestamptz NOT NULL CHECK(day_end>day_start),
 original_job_id uuid NOT NULL UNIQUE REFERENCES job(id),
 config_snapshot jsonb NOT NULL,
 config_version bigint NOT NULL,
 source_version bigint NOT NULL,
 binding_digest text NOT NULL,
 prepared_execution_id uuid,
 observation_job_id uuid UNIQUE REFERENCES job(id),
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','prepared','attended_observed','review_required')),
 safe_code text NOT NULL DEFAULT '' CHECK(safe_code IN ('','auth_required','blocked','rate_limited','bad_response','unavailable')),
 observed_at timestamptz,
 restore_id uuid,
 restore_operator text,
 restore_evidence text,
 restore_recorded_at timestamptz,
 restore_decision text,
 UNIQUE(indexer_id,expected_account_id,calendar_day),
 FOREIGN KEY(indexer_id,config_version) REFERENCES site_checkin_config_receipt(indexer_id,version),
 CHECK((status='attended_observed')=(observed_at IS NOT NULL)),
 CHECK((restore_id IS NULL AND restore_operator IS NULL AND restore_evidence IS NULL AND restore_recorded_at IS NULL AND restore_decision IS NULL)
 OR (restore_id IS NOT NULL AND length(btrim(restore_operator))>0 AND length(btrim(restore_evidence))>0 AND restore_recorded_at IS NOT NULL AND restore_decision='retain_review'))
);
CREATE TABLE site_checkin_execution (
 id uuid PRIMARY KEY,
 day_id uuid NOT NULL REFERENCES site_checkin_day(id),
 job_id uuid NOT NULL REFERENCES job(id),
 owner text NOT NULL,
 token bigint NOT NULL,
 nonce uuid NOT NULL,
 backend_pid integer NOT NULL,
 backend_start timestamptz NOT NULL,
 mode text NOT NULL CHECK(mode IN ('send','observe')),
 config_version bigint NOT NULL,
 source_version bigint NOT NULL,
 source_fingerprint text NOT NULL,
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','prepared','attended_observed','not_sent','review_required')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 observe_started_at timestamptz,
 prepared_at timestamptz,
 permission_id uuid UNIQUE,
 valid_until timestamptz,
 finished_at timestamptz,
 result jsonb,
 safe_code text NOT NULL DEFAULT '' CHECK(safe_code IN ('','auth_required','blocked','rate_limited','bad_response','unavailable')),
 UNIQUE(job_id,token), UNIQUE(day_id,id),
 CHECK((prepared_at IS NULL AND permission_id IS NULL AND valid_until IS NULL) OR
 (prepared_at IS NOT NULL AND permission_id IS NOT NULL AND valid_until>prepared_at AND mode='send')),
 CHECK((state IN ('attended_observed','not_sent','review_required'))=(finished_at IS NOT NULL)),
 CHECK((finished_at IS NULL)=(result IS NULL))
);
CREATE UNIQUE INDEX site_checkin_once ON site_checkin_execution(day_id) WHERE prepared_at IS NOT NULL;
ALTER TABLE site_checkin_day ADD CONSTRAINT site_checkin_prepared_parent FOREIGN KEY(id,prepared_execution_id) REFERENCES site_checkin_execution(day_id,id) DEFERRABLE INITIALLY DEFERRED;
-- +goose StatementBegin
CREATE FUNCTION pulse_checkin_binding(source_id uuid) RETURNS text LANGUAGE sql STABLE STRICT SET timezone='UTC' AS $$
 SELECT encode(sha256(convert_to(jsonb_build_object('id',i.id,'version',i.config_version,'url',i.url,
 'cipher',encode(i.api_key_encrypted,'hex'),'configuration',to_jsonb(ic),
 'lifecycle',jsonb_build_object('enabled',l.enabled,'deleted_at',l.deleted_at),
 'checkin',to_jsonb(c))::text,'UTF8')),'hex')
 FROM indexer i JOIN indexer_configuration ic ON ic.indexer_id=i.id
 JOIN site_checkin_config c ON c.indexer_id=i.id LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id WHERE i.id=source_id
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_checkin_identity() RETURNS TABLE(pid integer, started timestamptz) LANGUAGE sql STABLE AS $$
 SELECT pid,backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_checkin_history() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e site_checkin_execution;
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'checkin history protected'; END IF;
 IF TG_TABLE_NAME='site_checkin_day' THEN
  IF (to_jsonb(NEW)-ARRAY['prepared_execution_id','observation_job_id','status','safe_code','observed_at','restore_id','restore_operator','restore_evidence','restore_recorded_at','restore_decision']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['prepared_execution_id','observation_job_id','status','safe_code','observed_at','restore_id','restore_operator','restore_evidence','restore_recorded_at','restore_decision'])
   OR (OLD.prepared_execution_id IS NOT NULL AND NEW.prepared_execution_id IS DISTINCT FROM OLD.prepared_execution_id)
   OR (OLD.observation_job_id IS NOT NULL AND NEW.observation_job_id IS DISTINCT FROM OLD.observation_job_id)
   OR (OLD.observed_at IS NOT NULL AND (NEW.status,NEW.observed_at) IS DISTINCT FROM (OLD.status,OLD.observed_at))
  THEN RAISE EXCEPTION 'checkin day immutable'; END IF;
  IF NEW.prepared_execution_id IS NOT NULL THEN
   SELECT * INTO STRICT e FROM site_checkin_execution WHERE id=NEW.prepared_execution_id;
   IF e.day_id<>NEW.id OR e.mode<>'send' OR e.prepared_at IS NULL THEN RAISE EXCEPTION 'invalid prepared parent'; END IF;
  END IF;
  IF NEW.status='attended_observed' AND NOT EXISTS(SELECT 1 FROM site_checkin_execution x WHERE x.day_id=NEW.id AND x.state='attended_observed' AND x.result->>'account_id'=NEW.expected_account_id AND x.result->>'mark'='attended') THEN RAISE EXCEPTION 'checkin day requires native evidence'; END IF;
  IF OLD.status='attended_observed' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'observed checkin day immutable'; END IF;
  IF OLD.restore_id IS NOT NULL AND (NEW.restore_id,NEW.restore_operator,NEW.restore_evidence,NEW.restore_recorded_at,NEW.restore_decision) IS DISTINCT FROM
    (OLD.restore_id,OLD.restore_operator,OLD.restore_evidence,OLD.restore_recorded_at,OLD.restore_decision) THEN RAISE EXCEPTION 'checkin reconciliation immutable'; END IF;
  IF OLD.restore_id IS NULL AND NEW.restore_id IS NOT NULL THEN
   IF NOT EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated AND restore_id=NEW.restore_id)
    OR (to_jsonb(NEW)-ARRAY['restore_id','restore_operator','restore_evidence','restore_recorded_at','restore_decision']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['restore_id','restore_operator','restore_evidence','restore_recorded_at','restore_decision'])
    OR EXISTS(SELECT 1 FROM job WHERE id IN (NEW.original_job_id,NEW.observation_job_id) AND state IN ('queued','running'))
    THEN RAISE EXCEPTION 'checkin reconciliation requires offline isolation'; END IF;
  END IF;
 ELSE
  IF (to_jsonb(NEW)-ARRAY['state','observe_started_at','prepared_at','permission_id','valid_until','finished_at','result','safe_code']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['state','observe_started_at','prepared_at','permission_id','valid_until','finished_at','result','safe_code'])
   OR (OLD.prepared_at IS NOT NULL AND (NEW.prepared_at,NEW.permission_id,NEW.valid_until) IS DISTINCT FROM (OLD.prepared_at,OLD.permission_id,OLD.valid_until))
   OR (OLD.observe_started_at IS NOT NULL AND NEW.observe_started_at IS DISTINCT FROM OLD.observe_started_at)
   OR (OLD.finished_at IS NOT NULL AND NEW IS DISTINCT FROM OLD)
   OR (OLD.state='prepared' AND NEW.state NOT IN ('prepared','attended_observed','review_required'))
  THEN RAISE EXCEPTION 'checkin execution immutable'; END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER site_checkin_day_history BEFORE UPDATE ON site_checkin_day FOR EACH ROW EXECUTE FUNCTION pulse_checkin_history();
CREATE TRIGGER site_checkin_day_delete BEFORE DELETE OR TRUNCATE ON site_checkin_day FOR EACH STATEMENT EXECUTE FUNCTION pulse_checkin_history();
CREATE TRIGGER site_checkin_execution_history BEFORE UPDATE ON site_checkin_execution FOR EACH ROW EXECUTE FUNCTION pulse_checkin_history();
CREATE TRIGGER site_checkin_execution_delete BEFORE DELETE OR TRUNCATE ON site_checkin_execution FOR EACH STATEMENT EXECUTE FUNCTION pulse_checkin_history();
-- +goose StatementBegin
CREATE FUNCTION pulse_checkin_commit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e site_checkin_execution; d site_checkin_day; j job; c site_checkin_config; n timestamptz;
BEGIN
 SELECT * INTO STRICT e FROM site_checkin_execution WHERE id=NEW.id;
 SELECT * INTO STRICT d FROM site_checkin_day WHERE id=e.day_id;
 SELECT * INTO STRICT j FROM job WHERE id=e.job_id;
 -- Recovery has no authority to send or claim a positive result.
 IF e.state IN ('not_sent','review_required') AND current_setting('pulse.checkin_recovery',true)='on'
 AND (j.state='dead' OR (e.prepared_at IS NULL AND j.state='queued' AND j.attempts<j.max_attempts))
 AND (OLD.id IS NOT NULL AND OLD.finished_at IS NULL)
 AND (j.lease_until IS NULL OR j.lease_until<=clock_timestamp())
 AND e.result='{}'::jsonb THEN RETURN NULL; END IF;
 SELECT * INTO STRICT c FROM site_checkin_config WHERE indexer_id=d.indexer_id;
 n:=clock_timestamp();
 IF EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated)
 OR j.kind<>(CASE e.mode WHEN 'send' THEN 'site_checkin' ELSE 'site_checkin_observe' END)
 OR j.state<>(CASE WHEN e.finished_at IS NULL THEN 'running' WHEN e.state='attended_observed' THEN 'succeeded' ELSE 'dead' END)
 OR j.lease_owner IS DISTINCT FROM e.owner OR j.lease_token<>e.token OR j.lease_until<=n OR j.lease_until IS NULL
 OR NOT c.enabled OR c.version<>e.config_version OR c.source_config_version<>e.source_version
 OR pulse_checkin_binding(d.indexer_id) IS DISTINCT FROM e.source_fingerprint
 OR d.binding_digest IS DISTINCT FROM e.source_fingerprint
 OR NOT pulse_stats_gate_held(d.indexer_id)
 OR e.backend_pid<>pg_backend_pid() OR NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=e.backend_pid AND backend_start=e.backend_start)
 OR current_setting('pulse.checkin_nonce',true) IS DISTINCT FROM e.nonce::text
 OR n<d.day_start OR n>=d.day_end OR n<e.created_at OR (e.observe_started_at IS NOT NULL AND n<e.observe_started_at)
 OR EXISTS(SELECT 1 FROM indexer_lifecycle WHERE indexer_id=d.indexer_id AND (NOT enabled OR deleted_at IS NOT NULL))
 OR (e.prepared_at IS NOT NULL AND (d.prepared_execution_id IS DISTINCT FROM e.id AND e.mode='send'))
 OR (e.state='prepared' AND (n+interval '15 seconds'>e.valid_until OR e.valid_until>LEAST(d.day_end-interval '10 seconds',j.lease_until-interval '10 seconds')))
 OR (e.state='attended_observed' AND (e.result->>'account_id' IS DISTINCT FROM d.expected_account_id OR e.result->>'mark' IS DISTINCT FROM 'attended'))
 THEN RAISE EXCEPTION 'checkin execution fence expired'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER site_checkin_execution_commit AFTER INSERT OR UPDATE ON site_checkin_execution DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_checkin_commit();
REVOKE ALL ON site_checkin_day,site_checkin_execution FROM PUBLIC;
-- +goose Down
LOCK TABLE site_checkin_day,site_checkin_execution IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM site_checkin_day) OR EXISTS(SELECT 1 FROM site_checkin_execution) THEN RAISE EXCEPTION 'cannot drop checkin runtime history'; END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE site_checkin_day DROP CONSTRAINT site_checkin_prepared_parent;
DROP TABLE site_checkin_execution,site_checkin_day;
DROP FUNCTION pulse_checkin_commit(),pulse_checkin_history(),pulse_checkin_identity(),pulse_checkin_binding(uuid),pulse_checkin_calendar(timestamptz,text,text);