-- +goose Up
-- Permanent authorization survives cancellation and connection loss. A command
-- with no completed trusted return blocks further maintenance on its root.
CREATE TABLE library_maintenance_command (
 id UUID PRIMARY KEY, actor_id UUID NOT NULL REFERENCES app_user(id), request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 action TEXT NOT NULL CHECK(action IN ('reorganize','restore','cleanup')), input JSONB NOT NULL, request_hash TEXT NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 root_id UUID NOT NULL REFERENCES library_root(id),file_id UUID NOT NULL REFERENCES library_file(id),recycle_id TEXT,
 frozen JSONB NOT NULL CHECK(jsonb_typeof(frozen)='object'),created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),UNIQUE(actor_id,request_key)
);
CREATE TABLE library_maintenance_permit (
 command_id UUID PRIMARY KEY REFERENCES library_maintenance_command(id),owner UUID NOT NULL,token UUID NOT NULL,backend_pid INT NOT NULL,backend_start TIMESTAMPTZ NOT NULL,nonce UUID NOT NULL,
 started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),lease_until TIMESTAMPTZ NOT NULL,CHECK(lease_until>started_at AND lease_until<=started_at+interval '60 seconds')
);
CREATE TABLE library_maintenance_return (
 command_id UUID PRIMARY KEY REFERENCES library_maintenance_permit(command_id),owner UUID NOT NULL,token UUID NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('completed','unknown_requires_review')),result JSONB NOT NULL,after_state JSONB NOT NULL,
 audit_id UUID NOT NULL REFERENCES audit_log(id),event_id UUID NOT NULL REFERENCES outbox_event(id),returned_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION pulse_library_maintenance_snapshot(fid UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT jsonb_build_object('file',to_jsonb(f),'root',to_jsonb(r),
 'baseline',(SELECT jsonb_build_object('device',sf.root_device,'inode',sf.root_inode) FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id WHERE sf.root_id=r.id AND sr.generation=r.generation AND sr.state='succeeded' AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1),
 'mappings',COALESCE((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.unit_id) FROM library_file_unit x WHERE x.file_id=f.id),'[]'),
 'facts',COALESCE((SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE x.source_file_id=f.id),'[]'),
 'units',COALESCE((SELECT jsonb_agg(to_jsonb(u) ORDER BY u.id) FROM media_unit u WHERE u.id IN(SELECT unit_id FROM library_file_unit WHERE file_id=f.id)),'[]'),
 'media',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM media m WHERE m.id IN(SELECT u.media_id FROM media_unit u JOIN library_file_unit fu ON fu.unit_id=u.id WHERE fu.file_id=f.id)),'[]'),
 'lanes',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM acquisition_reservation a WHERE a.released_at IS NULL AND a.unit_id IN(SELECT unit_id FROM library_file_unit WHERE file_id=f.id)),'[]'),
 'reservations',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM unit_reservation a WHERE a.root_id=r.id AND a.state IN ('reserved','dispatched') AND a.unit_id IN(SELECT unit_id FROM library_file_unit WHERE file_id=f.id)),'[]'),
 'transfers',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM transfer_plan p WHERE p.root_id=r.id AND p.state<>'completed' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_preparation prep JOIN movie_upgrade_organization_ingestion imported USING(operation_id) WHERE prep.plan_id=p.id AND imported.status='imported_pending_retirement')),'[]'))
 FROM library_file f JOIN library_root r ON r.id=f.root_id WHERE f.id=fid
$$;
CREATE FUNCTION pulse_library_maintenance_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c library_maintenance_command; p library_maintenance_permit; observed JSONB;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'maintenance history is immutable'; END IF;
 IF TG_TABLE_NAME='library_maintenance_command' THEN
  IF NOT EXISTS(SELECT 1 FROM app_user WHERE id=NEW.actor_id AND is_active AND role='admin')
   OR NEW.frozen->'source_state' IS DISTINCT FROM pulse_library_maintenance_snapshot(NEW.file_id)
   OR NEW.frozen->'source_state'->'root'->>'id' IS DISTINCT FROM NEW.root_id::text
   OR NEW.input->>'action' IS DISTINCT FROM NEW.action
   OR EXISTS(SELECT 1 FROM library_maintenance_command x LEFT JOIN library_maintenance_return y ON y.command_id=x.id WHERE x.root_id=NEW.root_id AND x.id<>NEW.id AND (y.command_id IS NULL OR y.status<>'completed'))
  THEN RAISE EXCEPTION 'current maintenance authority required'; END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO STRICT c FROM library_maintenance_command WHERE id=NEW.command_id;
 IF TG_TABLE_NAME='library_maintenance_permit' THEN
  IF NEW.backend_pid<>pg_backend_pid() OR NEW.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
   OR NEW.nonce::text IS DISTINCT FROM current_setting('pulse.maintenance_nonce',true)
   OR NEW.lease_until<=clock_timestamp() OR c.frozen->'source_state' IS DISTINCT FROM pulse_library_maintenance_snapshot(c.file_id)
  THEN RAISE EXCEPTION 'maintenance physical connection required'; END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO STRICT p FROM library_maintenance_permit WHERE command_id=NEW.command_id;
 IF NEW.owner<>p.owner OR NEW.token<>p.token OR p.backend_pid<>pg_backend_pid()
  OR p.backend_start IS DISTINCT FROM (SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
  OR p.nonce::text IS DISTINCT FROM current_setting('pulse.maintenance_nonce',true)
  OR NEW.result->>'status' IS DISTINCT FROM NEW.status OR NEW.result->>'command_id' IS DISTINCT FROM c.id::text
  OR NOT EXISTS(SELECT 1 FROM audit_log a WHERE a.id=NEW.audit_id AND a.actor_id=c.actor_id::text AND a.action='library.maintenance.'||c.action AND a.target_id=c.id::text AND a.payload=NEW.result)
  OR NOT EXISTS(SELECT 1 FROM outbox_event e WHERE e.id=NEW.event_id AND e.event_type='library.maintenance.'||c.action AND e.aggregate_id=c.id::text AND e.payload=NEW.result)
 THEN RAISE EXCEPTION 'trusted maintenance return required'; END IF;
 observed:=jsonb_build_object('source',pulse_library_maintenance_snapshot(c.file_id),'replacement',CASE WHEN c.frozen->>'replacement_id' IS NOT NULL THEN pulse_library_maintenance_snapshot((c.frozen->>'replacement_id')::uuid) ELSE 'null'::jsonb END,'restored',CASE WHEN c.action='restore' AND NEW.status='completed' THEN pulse_library_maintenance_snapshot((NEW.result->>'file_id')::uuid) ELSE 'null'::jsonb END);
 IF NEW.after_state IS DISTINCT FROM observed THEN RAISE EXCEPTION 'maintenance return changed business state'; END IF;
 IF NEW.status='completed' THEN
  IF p.lease_until<=clock_timestamp() OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=c.actor_id AND is_active AND role='admin') OR NEW.after_state IS DISTINCT FROM observed OR EXISTS(SELECT 1 FROM unit_fact x WHERE x.source_file_id IN(c.file_id,(c.frozen->>'replacement_id')::uuid,(NEW.result->>'file_id')::uuid) AND x.revoked_at IS NULL AND x.expires_at<=clock_timestamp()) THEN RAISE EXCEPTION 'maintenance state changed at commit'; END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER library_maintenance_command_insert BEFORE INSERT ON library_maintenance_command FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_command_immutable BEFORE UPDATE OR DELETE ON library_maintenance_command FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_command_truncate BEFORE TRUNCATE ON library_maintenance_command FOR EACH STATEMENT EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_permit_insert BEFORE INSERT ON library_maintenance_permit FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_permit_immutable BEFORE UPDATE OR DELETE ON library_maintenance_permit FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_permit_truncate BEFORE TRUNCATE ON library_maintenance_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_return_insert BEFORE INSERT ON library_maintenance_return FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_return_immutable BEFORE UPDATE OR DELETE ON library_maintenance_return FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE TRIGGER library_maintenance_return_truncate BEFORE TRUNCATE ON library_maintenance_return FOR EACH STATEMENT EXECUTE FUNCTION pulse_library_maintenance_guard();
CREATE CONSTRAINT TRIGGER library_maintenance_return_commit AFTER INSERT ON library_maintenance_return DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_library_maintenance_guard();

-- +goose Down
LOCK TABLE library_maintenance_command,library_maintenance_permit,library_maintenance_return IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM library_maintenance_command) THEN RAISE EXCEPTION 'maintenance history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE library_maintenance_return,library_maintenance_permit,library_maintenance_command;
DROP FUNCTION pulse_library_maintenance_guard();
DROP FUNCTION pulse_library_maintenance_snapshot(UUID);
