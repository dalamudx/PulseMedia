-- +goose Up
CREATE TABLE cookiecloud_sync_schedule (
 owner_id UUID PRIMARY KEY REFERENCES cookiecloud_configuration(owner_id),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 next_due_at TIMESTAMPTZ NOT NULL,
 active_job_id UUID REFERENCES job(id),
 state TEXT NOT NULL CHECK(state IN ('idle','queued','running','review_required')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK((state IN ('queued','running'))=(active_job_id IS NOT NULL))
);
CREATE TABLE cookiecloud_sync_attempt (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 owner_id UUID NOT NULL REFERENCES cookiecloud_configuration(owner_id),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','review_required')),
 execution_owner TEXT,
 execution_token BIGINT,
 execution_pid INTEGER,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 started_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ,
 valid_until TIMESTAMPTZ,
 result JSONB,
 failure_code TEXT NOT NULL DEFAULT '' CHECK(failure_code IN ('','auth_required','blocked','rate_limited','bad_response','unavailable','rejected','forbidden','configuration_changed','lease_lost')),
 CHECK((execution_owner IS NULL)=(execution_token IS NULL)),
 CHECK((execution_owner IS NULL)=(execution_pid IS NULL)),
 CHECK(execution_owner IS NULL OR (length(execution_owner)>0 AND execution_token>0 AND execution_pid>0)),
 CHECK((state IN ('succeeded','failed','review_required'))=(finished_at IS NOT NULL)),
 CHECK((state='succeeded')=(result IS NOT NULL)),
 CHECK(result IS NULL OR (jsonb_typeof(result)='object' AND result ?& ARRAY['updated','unchanged','unmatched','rejected'] AND result-ARRAY['updated','unchanged','unmatched','rejected']='{}'::jsonb
  AND result->>'updated' ~ '^(0|[1-9][0-9]{0,4})$' AND result->>'unchanged' ~ '^(0|[1-9][0-9]{0,4})$'
  AND result->>'unmatched' ~ '^(0|[1-9][0-9]{0,4})$' AND result->>'rejected' ~ '^(0|[1-9][0-9]{0,4})$')),
 CHECK(state NOT IN ('running','succeeded','failed') OR (started_at IS NOT NULL AND execution_owner IS NOT NULL))
);
CREATE UNIQUE INDEX cookiecloud_sync_active_owner ON cookiecloud_sync_attempt(owner_id) WHERE state IN ('queued','running');
CREATE INDEX cookiecloud_sync_owner_history ON cookiecloud_sync_attempt(owner_id,created_at DESC,job_id);
-- +goose StatementBegin
CREATE FUNCTION pulse_cookiecloud_sync_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'CookieCloud synchronization history protected'; END IF;
 IF (NEW.job_id,NEW.owner_id,NEW.config_version,NEW.created_at) IS DISTINCT FROM (OLD.job_id,OLD.owner_id,OLD.config_version,OLD.created_at)
 OR (OLD.execution_owner IS NOT NULL AND (NEW.execution_owner,NEW.execution_token,NEW.execution_pid) IS DISTINCT FROM (OLD.execution_owner,OLD.execution_token,OLD.execution_pid))
 OR (OLD.started_at IS NOT NULL AND NEW.started_at IS DISTINCT FROM OLD.started_at)
 OR (OLD.state IN ('succeeded','failed','review_required') AND NEW IS DISTINCT FROM OLD)
 OR (OLD.state='running' AND NEW.state='queued')
 THEN RAISE EXCEPTION 'CookieCloud synchronization execution immutable'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER cookiecloud_sync_attempt_update BEFORE UPDATE ON cookiecloud_sync_attempt FOR EACH ROW EXECUTE FUNCTION pulse_cookiecloud_sync_history_guard();
CREATE TRIGGER cookiecloud_sync_attempt_history BEFORE DELETE OR TRUNCATE ON cookiecloud_sync_attempt FOR EACH STATEMENT EXECUTE FUNCTION pulse_cookiecloud_sync_history_guard();
-- Configuration and source authorization facts only; health and budget counters
-- are observations and do not invalidate an in-flight read.
-- +goose StatementBegin
CREATE FUNCTION pulse_cookiecloud_sync_binding(owner UUID) RETURNS TEXT LANGUAGE SQL STABLE STRICT AS $$
 SELECT encode(sha256(convert_to(jsonb_build_object('configuration',to_jsonb(c),'owner',jsonb_build_object('id',u.id,'role',u.role,'active',u.is_active),'sources',COALESCE((
  SELECT jsonb_agg(jsonb_build_object('id',i.id,'config_version',i.config_version,'url',i.url,'credential_ciphertext',encode(i.api_key_encrypted,'hex'),'configuration',to_jsonb(ic)) ORDER BY i.id)
  FROM indexer i JOIN indexer_configuration ic ON ic.indexer_id=i.id LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id
  WHERE ic.protocol='nexusphp' AND COALESCE(l.enabled,true) AND l.deleted_at IS NULL
 ),'[]'::jsonb))::text,'UTF8')),'hex')
 FROM cookiecloud_configuration c JOIN app_user u ON u.id=c.owner_id WHERE c.owner_id=owner;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_cookiecloud_sync_gate_held(owner UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted
  AND pid=pg_backend_pid() AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
  AND classid=((hashtextextended('cookiecloud:'||owner::text,0)>>32)&4294967295)::oid
  AND objid=(hashtextextended('cookiecloud:'||owner::text,0)&4294967295)::oid);
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_cookiecloud_sync_commit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a cookiecloud_sync_attempt; j job; c cookiecloud_configuration; s cookiecloud_sync_schedule;
BEGIN
 IF EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated) THEN RAISE EXCEPTION 'restore isolation active'; END IF;
 SELECT * INTO STRICT a FROM cookiecloud_sync_attempt WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT j FROM job WHERE id=a.job_id;
 SELECT * INTO STRICT s FROM cookiecloud_sync_schedule WHERE owner_id=a.owner_id;
 IF j.kind<>'cookiecloud_sync' OR j.max_attempts<>1 OR j.payload<>jsonb_build_object('owner_id',a.owner_id,'config_version',a.config_version)
 THEN RAISE EXCEPTION 'CookieCloud synchronization task binding invalid'; END IF;
 IF a.state='review_required' THEN
  IF j.state<>'dead' OR s.active_job_id IS NOT NULL OR s.state<>'review_required' THEN RAISE EXCEPTION 'CookieCloud recovery protection invalid'; END IF;
  RETURN NULL;
 END IF;
 SELECT * INTO STRICT c FROM cookiecloud_configuration WHERE owner_id=a.owner_id;
 IF NOT c.enabled OR c.version<>a.config_version OR s.config_version<>a.config_version
 OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=a.owner_id AND is_active AND role='admin')
 THEN RAISE EXCEPTION 'CookieCloud configuration authority changed'; END IF;
 IF a.state='queued' AND a.execution_owner IS NULL THEN
  IF j.state<>'queued' OR s.state<>'queued' OR s.active_job_id IS DISTINCT FROM a.job_id THEN RAISE EXCEPTION 'CookieCloud initial task graph invalid'; END IF;
  RETURN NULL;
 END IF;
 IF j.state<>(CASE a.state WHEN 'queued' THEN 'running' WHEN 'running' THEN 'running' WHEN 'succeeded' THEN 'succeeded' ELSE 'dead' END)
 OR j.lease_owner IS DISTINCT FROM a.execution_owner OR j.lease_token IS DISTINCT FROM a.execution_token
 OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp() OR a.execution_pid IS DISTINCT FROM pg_backend_pid()
 OR NOT pulse_cookiecloud_sync_gate_held(a.owner_id)
 OR pulse_cookiecloud_sync_binding(a.owner_id) IS DISTINCT FROM current_setting('pulse.cookiecloud_binding',true)
 OR (a.state IN ('queued','running') AND (s.active_job_id IS DISTINCT FROM a.job_id OR s.state<>a.state))
 OR (a.state IN ('succeeded','failed') AND (s.active_job_id IS NOT NULL OR s.state<>'idle'))
 OR (a.state='succeeded' AND (j.result IS DISTINCT FROM a.result OR (a.valid_until IS NOT NULL AND a.valid_until<=clock_timestamp())))
 THEN RAISE EXCEPTION 'CookieCloud synchronization execution fence expired'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER cookiecloud_sync_commit AFTER INSERT OR UPDATE ON cookiecloud_sync_attempt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_cookiecloud_sync_commit_guard();
REVOKE ALL ON cookiecloud_sync_schedule,cookiecloud_sync_attempt FROM PUBLIC;

-- +goose Down
LOCK TABLE cookiecloud_sync_schedule,cookiecloud_sync_attempt IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM cookiecloud_sync_attempt) OR EXISTS(SELECT 1 FROM cookiecloud_sync_schedule) THEN RAISE EXCEPTION 'cannot drop CookieCloud synchronization history'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE cookiecloud_sync_attempt,cookiecloud_sync_schedule;
DROP FUNCTION pulse_cookiecloud_sync_history_guard(),pulse_cookiecloud_sync_commit_guard(),pulse_cookiecloud_sync_binding(UUID),pulse_cookiecloud_sync_gate_held(UUID);
