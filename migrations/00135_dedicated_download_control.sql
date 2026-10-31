-- +goose Up
CREATE TABLE download_control_physical (
 operation_id UUID PRIMARY KEY,
 token UUID NOT NULL UNIQUE,
 scope TEXT NOT NULL CHECK(scope IN ('automatic-tv','complete-tv','movie-upgrade')),
 backend_pid INTEGER NOT NULL,
 backend_start TIMESTAMPTZ NOT NULL,
 advisory_key BIGINT NOT NULL
);
CREATE TABLE download_control_authority (
 command_id UUID PRIMARY KEY REFERENCES download_control_command(id),
 physical_token UUID NOT NULL,
 scope TEXT NOT NULL CHECK(scope IN ('automatic-tv','complete-tv','movie-upgrade')),
 digest TEXT NOT NULL CHECK(digest COLLATE "C" ~ '^[0-9a-f]{64}$')
);
CREATE TRIGGER download_control_authority_history BEFORE UPDATE OR DELETE ON download_control_authority FOR EACH ROW EXECUTE FUNCTION pulse_download_control_history();
CREATE TRIGGER download_control_authority_truncate BEFORE TRUNCATE ON download_control_authority FOR EACH STATEMENT EXECUTE FUNCTION pulse_download_control_history();
CREATE TRIGGER download_control_physical_history BEFORE UPDATE ON download_control_physical FOR EACH ROW EXECUTE FUNCTION pulse_download_control_history();
CREATE TRIGGER download_control_physical_truncate BEFORE TRUNCATE ON download_control_physical FOR EACH STATEMENT EXECUTE FUNCTION pulse_download_control_history();
-- +goose StatementBegin
CREATE FUNCTION pulse_download_control_dedicated_lock(pid INTEGER,k BIGINT) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT $1=pg_backend_pid() AND EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted AND pg_locks.pid=$1 AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=(($2>>32)&4294967295)::oid AND objid=($2&4294967295)::oid);
$$;
CREATE FUNCTION pulse_download_control_physical_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE k BIGINT; prefix TEXT;
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()) OR NOT pulse_download_control_dedicated_lock(OLD.backend_pid,OLD.advisory_key)
   OR EXISTS(SELECT 1 FROM download_control_authority a WHERE a.physical_token=OLD.token AND NOT EXISTS(SELECT 1 FROM download_control_return r WHERE r.command_id=a.command_id)) THEN
   RAISE EXCEPTION 'authentic control physical return required' USING ERRCODE='23514';
  END IF;
  RETURN OLD;
 END IF;
 prefix=CASE NEW.scope WHEN 'automatic-tv' THEN 'automatic-tv' WHEN 'complete-tv' THEN 'complete-tv' WHEN 'movie-upgrade' THEN 'movie-upgrade' END;
 k=(('x'||substr(encode(sha256(convert_to('pulsemedia.'||prefix||'.physical.v1:'||NEW.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint);
 IF NEW.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()) OR NEW.advisory_key<>k OR NOT pulse_download_control_dedicated_lock(NEW.backend_pid,k)
  OR NOT EXISTS(SELECT 1 FROM download_task WHERE operation_id=NEW.operation_id AND (state='tracking' OR (NEW.scope='movie-upgrade' AND state='submitting')))
  OR (NEW.scope='automatic-tv' AND (NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=NEW.operation_id) OR EXISTS(SELECT 1 FROM automatic_tv_physical_attempt WHERE operation_id=NEW.operation_id AND finished_at IS NULL) OR EXISTS(SELECT 1 FROM automatic_tv_observation_permit WHERE operation_id=NEW.operation_id AND finished_at IS NULL)))
  OR (NEW.scope='complete-tv' AND (NOT EXISTS(SELECT 1 FROM complete_tv_execution_authority WHERE operation_id=NEW.operation_id) OR EXISTS(SELECT 1 FROM complete_tv_physical_permit WHERE operation_id=NEW.operation_id)))
  OR (NEW.scope='movie-upgrade' AND (EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id) WHERE o.operation_id=NEW.operation_id AND r.outcome IN ('completed','manual_review')) OR NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=NEW.operation_id AND status='confirmed') OR EXISTS(SELECT 1 FROM movie_upgrade_tracking_permit p WHERE p.operation_id=NEW.operation_id AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_result r WHERE r.observation_id=p.observation_id)) OR EXISTS(SELECT 1 FROM movie_upgrade_observation_permit p WHERE p.operation_id=NEW.operation_id AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_result r WHERE r.observation_id=p.observation_id)))) THEN
  RAISE EXCEPTION 'dedicated control physical binding rejected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_download_control_authority_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM download_control_command c JOIN download_control_physical p ON p.operation_id=c.operation_id WHERE c.id=NEW.command_id AND p.token=NEW.physical_token AND p.scope=NEW.scope AND c.backend_pid=p.backend_pid AND (p.backend_start=(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()) AND pulse_download_control_dedicated_lock(p.backend_pid,p.advisory_key))) THEN
  RAISE EXCEPTION 'original dedicated control permit required' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_download_control_dedicated_gate() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM download_control_physical WHERE operation_id=NEW.operation_id) THEN
  RAISE EXCEPTION 'download control physical return required' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
DO $$ DECLARE tbl TEXT; BEGIN
 FOREACH tbl IN ARRAY ARRAY['automatic_tv_physical_attempt','automatic_tv_observation_permit','complete_tv_physical_permit','movie_upgrade_send_authorization','movie_upgrade_send_dispatch','movie_upgrade_observation','movie_upgrade_observation_permit','movie_upgrade_tracking_observation','movie_upgrade_tracking_permit'] LOOP
  EXECUTE format('CREATE TRIGGER download_control_dedicated_gate BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION pulse_download_control_dedicated_gate()',tbl);
 END LOOP;
END $$;
-- +goose StatementEnd
CREATE TRIGGER download_control_physical_binding BEFORE INSERT OR DELETE ON download_control_physical FOR EACH ROW EXECUTE FUNCTION pulse_download_control_physical_binding();
CREATE TRIGGER download_control_authority_binding BEFORE INSERT ON download_control_authority FOR EACH ROW EXECUTE FUNCTION pulse_download_control_authority_binding();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_download_control_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE task download_task%ROWTYPE; stamp TEXT;
BEGIN
 SELECT * INTO STRICT task FROM download_task WHERE id=NEW.task_id FOR SHARE;
 SELECT pulse_global_rates_binding(NEW.client_id) INTO stamp;
 IF task.operation_id<>NEW.operation_id OR task.client_id<>NEW.client_id
 OR lower(task.info_hash)<>NEW.info_hash OR task.save_path<>NEW.save_path
 OR stamp IS DISTINCT FROM NEW.client_stamp OR NEW.backend_pid<>pg_backend_pid()
 OR NEW.expires_at<=clock_timestamp() OR NOT pulse_download_control_gate_held(NEW.task_id)
 OR NOT (EXISTS(SELECT 1 FROM download_task_client_binding b WHERE b.task_id=task.id AND b.client_id=NEW.client_id AND b.client_stamp=NEW.client_stamp)
  OR ((task.state='tracking' OR (task.state='submitting' AND EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=task.operation_id))) AND EXISTS(SELECT 1 FROM download_control_physical p WHERE p.operation_id=task.operation_id AND (p.backend_start=(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()) AND pulse_download_control_dedicated_lock(p.backend_pid,p.advisory_key)))))
 OR EXISTS(SELECT 1 FROM download_control_command c WHERE c.task_id=NEW.task_id AND NOT EXISTS(SELECT 1 FROM download_control_confirmation f WHERE f.command_id=c.id)) THEN
  RAISE EXCEPTION 'download control binding rejected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_download_control_authority_required() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM download_control_physical p WHERE p.operation_id=NEW.operation_id) AND NOT EXISTS(SELECT 1 FROM download_control_authority a JOIN download_control_physical p ON p.token=a.physical_token WHERE a.command_id=NEW.id AND p.operation_id=NEW.operation_id AND a.scope=p.scope) THEN
  RAISE EXCEPTION 'dedicated control authority required' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER download_control_authority_required AFTER INSERT ON download_control_command DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_download_control_authority_required();
-- +goose StatementBegin
CREATE FUNCTION pulse_download_control_dedicated_return() RETURNS TRIGGER LANGUAGE plpgsql AS $mp21$
BEGIN
 IF EXISTS(SELECT 1 FROM download_control_authority WHERE command_id=NEW.command_id) AND NOT EXISTS(SELECT 1 FROM download_control_authority a JOIN download_control_physical p ON p.token=a.physical_token WHERE a.command_id=NEW.command_id AND p.backend_start=(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()) AND pulse_download_control_dedicated_lock(p.backend_pid,p.advisory_key)) THEN
  RAISE EXCEPTION 'original dedicated physical backend return required' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$mp21$;
-- +goose StatementEnd
CREATE TRIGGER download_control_dedicated_return BEFORE INSERT ON download_control_return FOR EACH ROW EXECUTE FUNCTION pulse_download_control_dedicated_return();
-- +goose Down
-- +goose StatementBegin
DO $$ DECLARE tbl TEXT; BEGIN
 LOCK TABLE download_control_authority,download_control_physical IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM download_control_authority) OR EXISTS(SELECT 1 FROM download_control_physical) THEN RAISE EXCEPTION 'dedicated control evidence prevents downgrade'; END IF;
 FOREACH tbl IN ARRAY ARRAY['automatic_tv_physical_attempt','automatic_tv_observation_permit','complete_tv_physical_permit','movie_upgrade_send_authorization','movie_upgrade_send_dispatch','movie_upgrade_observation','movie_upgrade_observation_permit','movie_upgrade_tracking_observation','movie_upgrade_tracking_permit'] LOOP
  EXECUTE format('DROP TRIGGER download_control_dedicated_gate ON %I',tbl);
 END LOOP;
END $$;
-- +goose StatementEnd
DROP TRIGGER download_control_authority_required ON download_control_command;
DROP FUNCTION pulse_download_control_authority_required();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_download_control_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE task download_task%ROWTYPE; stamp TEXT;
BEGIN
 SELECT * INTO STRICT task FROM download_task WHERE id=NEW.task_id FOR SHARE;
 SELECT pulse_global_rates_binding(NEW.client_id) INTO stamp;
 IF task.operation_id<>NEW.operation_id OR task.client_id<>NEW.client_id
 OR lower(task.info_hash)<>NEW.info_hash OR task.save_path<>NEW.save_path
 OR stamp IS DISTINCT FROM NEW.client_stamp OR NEW.backend_pid<>pg_backend_pid()
 OR NEW.expires_at<=clock_timestamp() OR NOT pulse_download_control_gate_held(NEW.task_id)
 OR NOT EXISTS(SELECT 1 FROM download_task_client_binding b WHERE b.task_id=task.id AND b.client_id=NEW.client_id AND b.client_stamp=NEW.client_stamp)
 OR EXISTS(SELECT 1 FROM download_control_command c WHERE c.task_id=NEW.task_id AND NOT EXISTS(SELECT 1 FROM download_control_confirmation f WHERE f.command_id=c.id)) THEN
  RAISE EXCEPTION 'download control binding rejected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER download_control_dedicated_return ON download_control_return;
DROP FUNCTION pulse_download_control_dedicated_return();
DROP TABLE download_control_authority,download_control_physical;
DROP FUNCTION pulse_download_control_dedicated_gate();
DROP FUNCTION pulse_download_control_authority_binding();
DROP FUNCTION pulse_download_control_physical_binding();
DROP FUNCTION pulse_download_control_dedicated_lock(INTEGER,BIGINT);
