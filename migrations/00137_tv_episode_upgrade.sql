-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION pulse_tv_upgrade_backend_start() RETURNS timestamptz LANGUAGE sql STABLE AS $$
 SELECT backend_start FROM pg_catalog.pg_stat_activity WHERE pid=pg_backend_pid()
$$;
-- +goose StatementEnd
CREATE TABLE tv_episode_upgrade_authority (
 operation_id UUID PRIMARY KEY, actor_id UUID NOT NULL REFERENCES app_user(id), subscription_id UUID NOT NULL REFERENCES subscription(id),
 unit_id UUID NOT NULL REFERENCES media_unit(id),root_id UUID NOT NULL REFERENCES library_root(id),old_file_id UUID NOT NULL REFERENCES library_file(id),old_fact_id UUID NOT NULL REFERENCES unit_fact(id),candidate_id UUID NOT NULL REFERENCES release_candidate(id),client_id UUID NOT NULL REFERENCES download_client(id),
 version TEXT NOT NULL CHECK(version ~ '^[0-9a-f]{64}$'),snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),valid_until TIMESTAMPTZ NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE tv_episode_upgrade_phase (
 operation_id UUID NOT NULL REFERENCES tv_episode_upgrade_authority(operation_id),
 action TEXT NOT NULL CHECK(action IN ('reserve','prepare','send','organization-confirm','organize','retirement-confirm','retire') OR action ~ '^observe:[^[:cntrl:]]{1,128}$'),
 actor_id UUID NOT NULL REFERENCES app_user(id),request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),request_hash TEXT NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 result JSONB NOT NULL CHECK(COALESCE(jsonb_typeof(result)='object' AND result->'execution_allowed'='false'::jsonb,false)),evidence JSONB NOT NULL DEFAULT '{}',created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(operation_id,action),UNIQUE(actor_id,request_key)
);
CREATE TABLE tv_episode_upgrade_permit (
 operation_id UUID NOT NULL REFERENCES tv_episode_upgrade_authority(operation_id),action TEXT NOT NULL CHECK(action IN ('send','organize','retire') OR action ~ '^observe:[^[:cntrl:]]{1,128}$'),
 owner UUID NOT NULL,token UUID NOT NULL,backend_pid INT NOT NULL,backend_start TIMESTAMPTZ NOT NULL,lock_key BIGINT NOT NULL,nonce UUID NOT NULL,lease_until TIMESTAMPTZ NOT NULL,request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),request_hash TEXT NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(operation_id,action)
);
CREATE TABLE tv_episode_upgrade_return (
 operation_id UUID NOT NULL,action TEXT NOT NULL,owner UUID NOT NULL,token UUID NOT NULL,status TEXT NOT NULL CHECK(status IN ('confirmed','unknown')),evidence JSONB NOT NULL,returned_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(operation_id,action),FOREIGN KEY(operation_id,action) REFERENCES tv_episode_upgrade_permit(operation_id,action)
);
CREATE TRIGGER tv_upgrade_authority_immutable BEFORE UPDATE OR DELETE ON tv_episode_upgrade_authority FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_phase_immutable BEFORE UPDATE OR DELETE ON tv_episode_upgrade_phase FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_permit_immutable BEFORE UPDATE OR DELETE ON tv_episode_upgrade_permit FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_return_immutable BEFORE UPDATE OR DELETE ON tv_episode_upgrade_return FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_authority_truncate BEFORE TRUNCATE ON tv_episode_upgrade_authority FOR EACH STATEMENT EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_phase_truncate BEFORE TRUNCATE ON tv_episode_upgrade_phase FOR EACH STATEMENT EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_permit_truncate BEFORE TRUNCATE ON tv_episode_upgrade_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_episode_candidate_review_guard();
CREATE TRIGGER tv_upgrade_return_truncate BEFORE TRUNCATE ON tv_episode_upgrade_return FOR EACH STATEMENT EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_tv_upgrade_lock(op uuid) RETURNS bigint LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT ('x'||substr(replace(op::text,'-',''),1,16))::bit(64)::bigint # ('x5456455055504752')::bit(64)::bigint
$$;
CREATE FUNCTION pulse_tv_upgrade_session(op uuid,k bigint,n uuid) RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
 IF k<>pulse_tv_upgrade_lock(op) OR current_setting('pulse.tv_upgrade_nonce',true) IS DISTINCT FROM n::text THEN RETURN false; END IF;
 PERFORM pg_advisory_xact_lock_shared(k);
 IF NOT pg_advisory_unlock(k) THEN RETURN false; END IF;
 PERFORM pg_advisory_lock(k);
 RETURN true;
END $$;
CREATE FUNCTION pulse_tv_upgrade_permit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a tv_episode_upgrade_authority; b timestamptz;
BEGIN
 SELECT * INTO STRICT a FROM tv_episode_upgrade_authority WHERE operation_id=NEW.operation_id FOR SHARE;
 SELECT backend_start INTO STRICT b FROM pg_stat_activity WHERE pid=pg_backend_pid();
 IF NEW.owner='00000000-0000-0000-0000-000000000000'::uuid OR NEW.token='00000000-0000-0000-0000-000000000000'::uuid
 OR NEW.backend_pid<>pg_backend_pid() OR NEW.backend_start<>b OR NEW.lease_until<=clock_timestamp() OR NEW.lease_until>clock_timestamp()+interval '61 seconds'
 OR NEW.created_at>clock_timestamp() OR NOT pulse_tv_upgrade_session(NEW.operation_id,NEW.lock_key,NEW.nonce)
 OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=a.actor_id AND is_active AND role='admin')
 OR NOT EXISTS(SELECT 1 FROM subscription s WHERE s.id=a.subscription_id AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better'))
 OR EXISTS(SELECT 1 FROM tv_episode_upgrade_permit p LEFT JOIN tv_episode_upgrade_return r USING(operation_id,action) WHERE p.operation_id=NEW.operation_id AND p.action<>NEW.action AND r.operation_id IS NULL)
 THEN RAISE EXCEPTION 'TV upgrade original physical authority required'; END IF;
 IF NEW.action='send' AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_phase WHERE operation_id=NEW.operation_id AND action='prepare') THEN RAISE EXCEPTION 'TV upgrade preparation required'; END IF;
 IF NEW.action LIKE 'observe:%' AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_return WHERE operation_id=NEW.operation_id AND action='send') THEN RAISE EXCEPTION 'TV upgrade original return required'; END IF;
 IF NEW.action='organize' AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_phase WHERE operation_id=NEW.operation_id AND action='organization-confirm') THEN RAISE EXCEPTION 'TV upgrade organization confirmation required'; END IF;
 IF NEW.action='retire' AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_phase WHERE operation_id=NEW.operation_id AND action='retirement-confirm') THEN RAISE EXCEPTION 'TV upgrade retirement confirmation required'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_tv_upgrade_return_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p tv_episode_upgrade_permit; b timestamptz;
BEGIN
 SELECT * INTO STRICT p FROM tv_episode_upgrade_permit WHERE operation_id=NEW.operation_id AND action=NEW.action FOR SHARE;
 SELECT backend_start INTO STRICT b FROM pg_stat_activity WHERE pid=pg_backend_pid();
 IF NEW.owner<>p.owner OR NEW.token<>p.token OR p.backend_pid<>pg_backend_pid() OR p.backend_start<>b OR p.lease_until<=clock_timestamp() OR NEW.returned_at>clock_timestamp()
 OR NOT pulse_tv_upgrade_session(p.operation_id,p.lock_key,p.nonce) THEN RAISE EXCEPTION 'TV upgrade original physical return required'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_tv_upgrade_phase_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a tv_episode_upgrade_authority;
BEGIN
 SELECT * INTO STRICT a FROM tv_episode_upgrade_authority WHERE operation_id=NEW.operation_id FOR SHARE;
 IF NEW.actor_id<>a.actor_id OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=a.actor_id AND is_active AND role='admin')
 OR NEW.result->>'operation_id' IS DISTINCT FROM a.operation_id::text OR NEW.result->>'unit_id' IS DISTINCT FROM a.unit_id::text
 OR NEW.result->>'current_file_id' IS DISTINCT FROM a.old_file_id::text OR NEW.result->>'candidate_id' IS DISTINCT FROM a.candidate_id::text
 THEN RAISE EXCEPTION 'TV upgrade phase identity required'; END IF;
 IF (NEW.action IN ('send','organize','retire') OR NEW.action LIKE 'observe:%') AND NOT EXISTS(
 SELECT 1 FROM tv_episode_upgrade_return r JOIN tv_episode_upgrade_permit p USING(operation_id,action) JOIN pg_stat_activity b ON b.pid=pg_backend_pid()
 WHERE r.operation_id=NEW.operation_id AND r.action=NEW.action AND p.request_key=NEW.request_key AND p.request_hash=NEW.request_hash AND p.backend_pid=b.pid AND p.backend_start=b.backend_start AND p.lease_until>clock_timestamp() AND pulse_tv_upgrade_session(p.operation_id,p.lock_key,p.nonce)) THEN RAISE EXCEPTION 'TV upgrade authenticated return required'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_tv_episode_upgrade_no_generic_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM tv_episode_upgrade_authority WHERE operation_id=NEW.operation_id) THEN RAISE EXCEPTION 'TV episode upgrade requires dedicated authority'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER tv_upgrade_permit_insert BEFORE INSERT ON tv_episode_upgrade_permit FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_permit_guard();
CREATE TRIGGER tv_upgrade_return_insert BEFORE INSERT ON tv_episode_upgrade_return FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_return_guard();
CREATE TRIGGER tv_upgrade_phase_insert BEFORE INSERT ON tv_episode_upgrade_phase FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_phase_guard();
CREATE TRIGGER tv_episode_upgrade_no_generic_task BEFORE INSERT OR UPDATE ON download_task FOR EACH ROW EXECUTE FUNCTION pulse_tv_episode_upgrade_no_generic_task();
-- Reauthenticate lease, session identity and native return at transaction commit.
CREATE CONSTRAINT TRIGGER tv_upgrade_permit_commit AFTER INSERT ON tv_episode_upgrade_permit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_permit_guard();
CREATE CONSTRAINT TRIGGER tv_upgrade_return_commit AFTER INSERT ON tv_episode_upgrade_return DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_return_guard();
CREATE CONSTRAINT TRIGGER tv_upgrade_phase_commit AFTER INSERT ON tv_episode_upgrade_phase DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_phase_guard();
-- +goose Down
LOCK TABLE tv_episode_upgrade_authority,tv_episode_upgrade_phase,tv_episode_upgrade_permit,tv_episode_upgrade_return IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM tv_episode_upgrade_authority) THEN RAISE EXCEPTION 'TV episode upgrade history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TRIGGER tv_episode_upgrade_no_generic_task ON download_task;
DROP FUNCTION pulse_tv_episode_upgrade_no_generic_task();
DROP TABLE tv_episode_upgrade_return,tv_episode_upgrade_permit,tv_episode_upgrade_phase,tv_episode_upgrade_authority;
DROP FUNCTION pulse_tv_upgrade_phase_guard();
DROP FUNCTION pulse_tv_upgrade_return_guard();
DROP FUNCTION pulse_tv_upgrade_permit_guard();
DROP FUNCTION pulse_tv_upgrade_session(uuid,bigint,uuid);
DROP FUNCTION pulse_tv_upgrade_lock(uuid);
DROP FUNCTION pulse_tv_upgrade_backend_start();
