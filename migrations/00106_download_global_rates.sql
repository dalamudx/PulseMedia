-- +goose Up
CREATE TABLE download_global_rate_attempt (
 job_id UUID PRIMARY KEY REFERENCES job(id) ON DELETE RESTRICT,
 client_id UUID NOT NULL,
 config_stamp TEXT NOT NULL CHECK(config_stamp COLLATE "C" ~ '^[0-9a-f]{64}$'),
 state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','abandoned')),
 execution_owner TEXT,
 execution_token BIGINT CHECK(execution_token>0),
 started_at TIMESTAMPTZ,
 observed_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ,
 download_bytes_per_second BIGINT CHECK(download_bytes_per_second>=0),
 upload_bytes_per_second BIGINT CHECK(upload_bytes_per_second>=0),
 failure_code TEXT NOT NULL DEFAULT '' CHECK(failure_code IN ('','unavailable','auth_required','timeout','blocked','bad_response','rate_limited')),
 CHECK((state='queued' AND execution_owner IS NULL AND execution_token IS NULL AND started_at IS NULL AND observed_at IS NULL AND finished_at IS NULL AND failure_code='') OR
       (state='running' AND execution_owner IS NOT NULL AND execution_token IS NOT NULL AND started_at IS NOT NULL AND observed_at IS NULL AND finished_at IS NULL AND failure_code='') OR
       (state='succeeded' AND execution_owner IS NOT NULL AND execution_token IS NOT NULL AND started_at IS NOT NULL AND observed_at IS NOT NULL AND finished_at IS NOT NULL AND failure_code='') OR
       (state='failed' AND execution_owner IS NOT NULL AND execution_token IS NOT NULL AND started_at IS NOT NULL AND observed_at IS NOT NULL AND finished_at IS NOT NULL AND failure_code<>'') OR
       (state='abandoned' AND observed_at IS NULL AND finished_at IS NOT NULL AND failure_code='unavailable')),
 CHECK(state='succeeded' OR (download_bytes_per_second IS NULL AND upload_bytes_per_second IS NULL)),
 CHECK(execution_owner IS NULL OR length(execution_owner) BETWEEN 1 AND 160),
 CHECK(observed_at IS NULL OR observed_at>=started_at-interval '2 seconds')
);
CREATE INDEX download_global_rate_history ON download_global_rate_attempt(client_id,observed_at DESC,job_id) WHERE observed_at IS NOT NULL;
CREATE UNIQUE INDEX download_global_rate_one_active ON download_global_rate_attempt(client_id) WHERE state IN ('queued','running');

CREATE TABLE download_global_rate_schedule (
 client_id UUID PRIMARY KEY REFERENCES download_client(id) ON DELETE CASCADE,
 active_job_id UUID UNIQUE REFERENCES download_global_rate_attempt(job_id) DEFERRABLE INITIALLY DEFERRED,
 latest_job_id UUID REFERENCES download_global_rate_attempt(job_id) DEFERRABLE INITIALLY DEFERRED,
 last_good_job_id UUID REFERENCES download_global_rate_attempt(job_id) DEFERRABLE INITIALLY DEFERRED,
 next_due_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- +goose StatementBegin
CREATE FUNCTION pulse_global_rates_binding(p_client UUID) RETURNS TEXT LANGUAGE SQL STABLE AS $$
 SELECT encode(sha256(convert_to(jsonb_build_array(c.id,c.client_type,c.url,c.username,encode(c.password_encrypted,'hex'),COALESCE(c.remote_path_map,'{}'::jsonb))::text,'UTF8')),'hex') FROM download_client c WHERE c.id=p_client
$$;
CREATE FUNCTION pulse_global_rates_gate_held(p_client UUID) RETURNS BOOLEAN LANGUAGE SQL VOLATILE AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND mode='ExclusiveLock' AND granted AND objsubid=1
 AND classid=((hashtextextended('download-global-rates:'||p_client::text,0)>>32)&4294967295)::oid
 AND objid=(hashtextextended('download-global-rates:'||p_client::text,0)&4294967295)::oid)
$$;
CREATE FUNCTION pulse_assert_global_rates_attempt() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE a download_global_rate_attempt%ROWTYPE; j job%ROWTYPE; s download_global_rate_schedule%ROWTYPE; stamp TEXT;
BEGIN
 SELECT * INTO STRICT a FROM download_global_rate_attempt WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT j FROM job WHERE id=a.job_id FOR SHARE;
 IF j.kind<>'download_global_rates' OR j.max_attempts<>1 OR j.payload<>jsonb_build_object('client_id',a.client_id,'config_stamp',a.config_stamp) THEN
  RAISE EXCEPTION 'global rate job binding rejected' USING ERRCODE='23514';
 END IF;
 IF a.state='abandoned' THEN
  IF j.state<>'dead' THEN RAISE EXCEPTION 'global rate recovery graph rejected' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 PERFORM 1 FROM download_client WHERE id=a.client_id FOR SHARE;
 stamp:=pulse_global_rates_binding(a.client_id);
 IF stamp IS NULL OR stamp<>a.config_stamp THEN RAISE EXCEPTION 'global rate source changed' USING ERRCODE='23514'; END IF;
 SELECT * INTO STRICT s FROM download_global_rate_schedule WHERE client_id=a.client_id;
 IF a.state='queued' THEN
  IF j.state<>'queued' OR s.active_job_id IS DISTINCT FROM a.job_id THEN RAISE EXCEPTION 'global rate queue graph rejected' USING ERRCODE='23514'; END IF;
 ELSE
  IF NOT pulse_global_rates_gate_held(a.client_id) OR j.lease_owner IS DISTINCT FROM a.execution_owner OR j.lease_token IS DISTINCT FROM a.execution_token OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp() THEN
   RAISE EXCEPTION 'global rate execution lease rejected' USING ERRCODE='23514';
  END IF;
  IF a.state='running' AND (j.state<>'running' OR s.active_job_id IS DISTINCT FROM a.job_id) THEN RAISE EXCEPTION 'global rate running graph rejected' USING ERRCODE='23514'; END IF;
  IF a.state IN ('succeeded','failed') AND (j.state<>'succeeded' OR s.active_job_id IS NOT NULL OR s.latest_job_id IS DISTINCT FROM a.job_id OR a.observed_at>clock_timestamp()+interval '2 seconds') THEN
   RAISE EXCEPTION 'global rate terminal graph rejected' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER download_global_rate_graph AFTER INSERT OR UPDATE ON download_global_rate_attempt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_assert_global_rates_attempt();
CREATE FUNCTION pulse_global_rates_immutable() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.state IN ('succeeded','failed','abandoned') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'global rate terminal observation immutable' USING ERRCODE='23514'; END IF;
 IF NEW.job_id<>OLD.job_id OR NEW.client_id<>OLD.client_id OR NEW.config_stamp<>OLD.config_stamp OR (OLD.execution_owner IS NOT NULL AND (NEW.execution_owner IS DISTINCT FROM OLD.execution_owner OR NEW.execution_token IS DISTINCT FROM OLD.execution_token)) THEN
  RAISE EXCEPTION 'global rate execution binding immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER download_global_rate_immutable BEFORE UPDATE ON download_global_rate_attempt FOR EACH ROW EXECUTE FUNCTION pulse_global_rates_immutable();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM download_global_rate_attempt) OR EXISTS(SELECT 1 FROM download_global_rate_schedule) THEN RAISE EXCEPTION 'global rate history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE download_global_rate_schedule;
DROP TABLE download_global_rate_attempt;
DROP FUNCTION pulse_global_rates_immutable();
DROP FUNCTION pulse_assert_global_rates_attempt();
DROP FUNCTION pulse_global_rates_gate_held(UUID);
DROP FUNCTION pulse_global_rates_binding(UUID);
