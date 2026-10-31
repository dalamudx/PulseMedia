-- +goose Up
CREATE TABLE download_control_command (
 id UUID PRIMARY KEY,
 task_id UUID NOT NULL REFERENCES download_task(id),
 operation_id UUID NOT NULL,
 actor_id UUID NOT NULL REFERENCES app_user(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 160),
 action TEXT NOT NULL CHECK(action IN ('pause','resume')),
 affect_shared BOOLEAN NOT NULL,
 client_id UUID NOT NULL REFERENCES download_client(id),
 client_stamp TEXT NOT NULL CHECK(client_stamp COLLATE "C" ~ '^[0-9a-f]{64}$'),
 info_hash TEXT NOT NULL CHECK(info_hash COLLATE "C" ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
 save_path TEXT NOT NULL,
 requirements JSONB NOT NULL CHECK(jsonb_typeof(requirements)='array'),
 backend_pid INTEGER NOT NULL,
 nonce UUID NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,request_key)
);
CREATE INDEX download_control_command_task ON download_control_command(task_id);
CREATE TABLE download_control_return (
 command_id UUID PRIMARY KEY REFERENCES download_control_command(id),
 returned_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE download_control_confirmation (
 command_id UUID PRIMARY KEY REFERENCES download_control_return(command_id),
 source TEXT NOT NULL CHECK(source IN ('acknowledgement','observation')),
 confirmed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION pulse_download_control_gate_held(task UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted
 AND pid=pg_backend_pid() AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
 AND classid=((hashtextextended('download-control:'||task::text,0)>>32)&4294967295)::oid
 AND objid=(hashtextextended('download-control:'||task::text,0)&4294967295)::oid);
$$;
CREATE FUNCTION pulse_download_control_history() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'download control history is immutable' USING ERRCODE='23514';
END;
$$;
CREATE FUNCTION pulse_download_control_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
DO $$ DECLARE tbl TEXT; BEGIN
 FOREACH tbl IN ARRAY ARRAY['download_control_command','download_control_return','download_control_confirmation'] LOOP
  EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION pulse_download_control_history()',tbl||'_history',tbl);
  EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION pulse_download_control_history()',tbl||'_truncate',tbl);
 END LOOP;
END $$;
-- +goose StatementEnd
CREATE TRIGGER download_control_binding BEFORE INSERT ON download_control_command FOR EACH ROW EXECUTE FUNCTION pulse_download_control_binding();
-- +goose StatementBegin
CREATE FUNCTION pulse_download_control_return_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE c download_control_command%ROWTYPE;
BEGIN
 SELECT * INTO STRICT c FROM download_control_command WHERE id=NEW.command_id FOR SHARE;
 IF c.backend_pid<>pg_backend_pid() OR NOT pulse_download_control_gate_held(c.task_id) THEN
  RAISE EXCEPTION 'download control original execution required' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER download_control_return_binding BEFORE INSERT ON download_control_return FOR EACH ROW EXECUTE FUNCTION pulse_download_control_return_binding();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE download_control_command,download_control_return,download_control_confirmation IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM download_control_command) THEN RAISE EXCEPTION 'download control history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE download_control_confirmation,download_control_return,download_control_command;
DROP FUNCTION pulse_download_control_binding();
DROP FUNCTION pulse_download_control_return_binding();
DROP FUNCTION pulse_download_control_gate_held(UUID);
DROP FUNCTION pulse_download_control_history();
