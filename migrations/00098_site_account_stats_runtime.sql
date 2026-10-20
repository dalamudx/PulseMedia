-- +goose Up
CREATE TABLE site_account_stats_schedule (
 indexer_id UUID PRIMARY KEY REFERENCES site_account_stats_config(indexer_id),
 config_version BIGINT NOT NULL,
 next_due_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 active_job_id UUID REFERENCES job(id),
 state TEXT NOT NULL CHECK(state IN ('queued','running','idle','review_required')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(indexer_id,config_version) REFERENCES site_account_stats_config_receipt(indexer_id,version)
);
CREATE TABLE site_account_stats_attempt (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 indexer_id UUID NOT NULL REFERENCES site_account_stats_config(indexer_id),
 config_version BIGINT NOT NULL,
 source_version BIGINT NOT NULL CHECK(source_version>0),
 state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','review_required')),
 execution_owner TEXT,
 execution_token BIGINT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 started_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ,
 sample JSONB,
 failure_code TEXT NOT NULL DEFAULT '' CHECK(failure_code IN ('','auth_required','blocked','rate_limited','bad_response','unavailable')),
 FOREIGN KEY(indexer_id,config_version) REFERENCES site_account_stats_config_receipt(indexer_id,version),
 CHECK((execution_owner IS NULL)=(execution_token IS NULL)),
 CHECK((state IN ('succeeded','failed','review_required'))=(finished_at IS NOT NULL)),
 CHECK((state='succeeded')=(sample IS NOT NULL)),
 CHECK(state NOT IN ('running','succeeded','failed') OR (started_at IS NOT NULL AND execution_owner IS NOT NULL))
);
CREATE UNIQUE INDEX site_account_stats_active ON site_account_stats_attempt(indexer_id) WHERE state IN ('queued','running');
CREATE INDEX site_account_stats_history ON site_account_stats_attempt(indexer_id,created_at DESC,job_id);
-- +goose StatementBegin
CREATE FUNCTION pulse_stats_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'site statistics history protected'; END IF;
 IF (NEW.job_id,NEW.indexer_id,NEW.config_version,NEW.source_version,NEW.created_at) IS DISTINCT FROM
    (OLD.job_id,OLD.indexer_id,OLD.config_version,OLD.source_version,OLD.created_at)
 OR (OLD.execution_owner IS NOT NULL AND (NEW.execution_owner,NEW.execution_token) IS DISTINCT FROM (OLD.execution_owner,OLD.execution_token))
 OR (OLD.started_at IS NOT NULL AND NEW.started_at IS DISTINCT FROM OLD.started_at)
 OR (OLD.state IN ('succeeded','failed','review_required') AND NEW IS DISTINCT FROM OLD)
 OR (OLD.state='running' AND NEW.state='queued')
 THEN RAISE EXCEPTION 'site statistics attempt immutable'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER site_account_stats_attempt_update BEFORE UPDATE ON site_account_stats_attempt FOR EACH ROW EXECUTE FUNCTION pulse_stats_history_guard();
CREATE TRIGGER site_account_stats_attempt_history BEFORE DELETE OR TRUNCATE ON site_account_stats_attempt FOR EACH STATEMENT EXECUTE FUNCTION pulse_stats_history_guard();
-- The digest is transaction local; it contains no plaintext execution material.
-- +goose StatementBegin
CREATE FUNCTION pulse_stats_binding(id UUID) RETURNS TEXT LANGUAGE SQL STABLE STRICT AS $$
 SELECT encode(sha256(convert_to(jsonb_build_object('id',i.id,'name',i.name,'url',i.url,'api_key_encrypted',encode(i.api_key_encrypted,'hex'),'config_version',i.config_version,'categories',i.categories)::text || to_jsonb(ic)::text || to_jsonb(c)::text || to_jsonb(r)::text,'UTF8')),'hex')
 FROM indexer i JOIN indexer_configuration ic ON ic.indexer_id=i.id
 JOIN site_account_stats_config c ON c.indexer_id=i.id
 JOIN site_account_stats_config_receipt r ON r.indexer_id=c.indexer_id AND r.version=c.version WHERE i.id=id;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_stats_gate_held(id UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted
 AND pid=pg_backend_pid() AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
 AND classid=((hashtextextended('indexer:'||id::text,0)>>32)&4294967295)::oid
 AND objid=(hashtextextended('indexer:'||id::text,0)&4294967295)::oid);
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_stats_commit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a site_account_stats_attempt; j job; c site_account_stats_config; s site_account_stats_schedule;
BEGIN
 IF EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated) THEN RAISE EXCEPTION 'restore isolation active'; END IF;
 SELECT * INTO STRICT a FROM site_account_stats_attempt WHERE job_id=NEW.job_id;
 IF a.state IN ('queued','review_required') THEN RETURN NULL; END IF;
 SELECT * INTO STRICT j FROM job WHERE id=a.job_id;
 SELECT * INTO STRICT c FROM site_account_stats_config WHERE indexer_id=a.indexer_id;
 SELECT * INTO STRICT s FROM site_account_stats_schedule WHERE indexer_id=a.indexer_id;
 IF j.kind<>'site_account_stats_collect' OR j.max_attempts<>1
 OR j.payload<>jsonb_build_object('indexer_id',a.indexer_id,'config_version',a.config_version,'source_version',a.source_version)
 OR j.state<>(CASE a.state WHEN 'running' THEN 'running' WHEN 'succeeded' THEN 'succeeded' ELSE 'dead' END)
 OR j.lease_owner IS DISTINCT FROM a.execution_owner OR j.lease_token IS DISTINCT FROM a.execution_token
 OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
 OR NOT c.enabled OR c.version<>a.config_version OR c.source_config_version<>a.source_version
 OR NOT EXISTS(SELECT 1 FROM indexer WHERE id=a.indexer_id AND config_version=a.source_version)
 OR EXISTS(SELECT 1 FROM indexer_lifecycle WHERE indexer_id=a.indexer_id AND (NOT enabled OR deleted_at IS NOT NULL))
 OR NOT pulse_stats_gate_held(a.indexer_id)
 OR pulse_stats_binding(a.indexer_id) IS DISTINCT FROM current_setting('pulse.stats_binding',true)
 OR s.config_version<>a.config_version
 OR (a.state='running' AND (s.active_job_id IS DISTINCT FROM a.job_id OR s.state<>'running'))
 OR (a.state<>'running' AND (s.active_job_id IS NOT NULL OR s.state<>'idle'))
 OR (a.state='succeeded' AND a.sample->>'account_id' IS DISTINCT FROM c.expected_account_id)
 THEN RAISE EXCEPTION 'site statistics execution fence expired'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER site_account_stats_commit AFTER INSERT OR UPDATE ON site_account_stats_attempt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_stats_commit_guard();
REVOKE ALL ON site_account_stats_schedule,site_account_stats_attempt FROM PUBLIC;

-- +goose Down
LOCK TABLE site_account_stats_schedule,site_account_stats_attempt IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM site_account_stats_schedule) OR EXISTS(SELECT 1 FROM site_account_stats_attempt) THEN
 RAISE EXCEPTION 'cannot drop site statistics runtime history'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE site_account_stats_attempt,site_account_stats_schedule;
DROP FUNCTION pulse_stats_history_guard(),pulse_stats_commit_guard(),pulse_stats_binding(UUID),pulse_stats_gate_held(UUID);
