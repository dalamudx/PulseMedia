-- +goose Up
ALTER TABLE download_control_physical DROP CONSTRAINT download_control_physical_scope_check;
ALTER TABLE download_control_physical ADD CONSTRAINT download_control_physical_scope_check CHECK(scope IN ('automatic-tv','complete-tv','movie-upgrade','tv-episode-upgrade'));
ALTER TABLE download_control_authority DROP CONSTRAINT download_control_authority_scope_check;
ALTER TABLE download_control_authority ADD CONSTRAINT download_control_authority_scope_check CHECK(scope IN ('automatic-tv','complete-tv','movie-upgrade','tv-episode-upgrade'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_tv_episode_upgrade_no_generic_task() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a tv_episode_upgrade_authority; p jsonb; r tv_episode_upgrade_permit;
BEGIN
 SELECT * INTO a FROM tv_episode_upgrade_authority WHERE operation_id=CASE WHEN TG_OP='DELETE' THEN OLD.operation_id ELSE NEW.operation_id END;
 IF TG_OP='UPDATE' AND OLD.operation_id<>NEW.operation_id AND EXISTS(SELECT 1 FROM tv_episode_upgrade_authority WHERE operation_id=OLD.operation_id) THEN RAISE EXCEPTION 'TV control task cannot change operation'; END IF;
 IF NOT FOUND THEN IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF; END IF;
 IF TG_OP='DELETE' OR pg_trigger_depth()<>2 THEN RAISE EXCEPTION 'TV episode upgrade requires dedicated authority'; END IF;
 SELECT evidence INTO STRICT p FROM tv_episode_upgrade_phase WHERE operation_id=a.operation_id AND action='prepare';
 SELECT * INTO STRICT r FROM tv_episode_upgrade_permit WHERE operation_id=a.operation_id AND action=CASE WHEN TG_OP='INSERT' THEN 'send' ELSE 'organize' END;
 IF NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_return x WHERE x.operation_id=a.operation_id AND x.action=r.action AND x.owner=r.owner AND x.token=r.token AND x.status='confirmed')
 OR NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_phase x WHERE x.operation_id=a.operation_id AND x.action=r.action)
 OR r.backend_pid<>pg_backend_pid() OR r.backend_start<>pulse_tv_upgrade_backend_start() OR r.lease_until<=clock_timestamp() OR NOT pulse_tv_upgrade_session(a.operation_id,r.lock_key,r.nonce)
 OR NEW.client_id<>a.client_id OR NEW.candidate_id<>a.candidate_id OR NEW.info_hash IS DISTINCT FROM p->>'info_hash' OR NEW.save_path IS DISTINCT FROM p->>'save_path' OR NEW.total_bytes IS DISTINCT FROM (p->>'total')::bigint
 OR (TG_OP='INSERT' AND (NEW.state<>'tracking' OR NEW.progress<>0 OR NEW.downloaded_bytes<>0 OR NEW.content_path IS NOT NULL OR NEW.transfer_plan_id IS NOT NULL))
 OR (TG_OP='UPDATE' AND (OLD.state<>'tracking' OR NEW.state<>'completed' OR NEW.progress<>1 OR NEW.downloaded_bytes<>NEW.total_bytes OR NEW.organization_state<>'completed' OR (to_jsonb(OLD)-ARRAY['state','progress','downloaded_bytes','finished_at','updated_at','organization_state']::text[]) IS DISTINCT FROM (to_jsonb(NEW)-ARRAY['state','progress','downloaded_bytes','finished_at','updated_at','organization_state']::text[])))
 THEN RAISE EXCEPTION 'TV episode upgrade original task identity required'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_tv_upgrade_control_task() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a tv_episode_upgrade_authority; p jsonb;
BEGIN
 IF NEW.action='send' AND NEW.result->>'status'='tracking' THEN
  SELECT * INTO STRICT a FROM tv_episode_upgrade_authority WHERE operation_id=NEW.operation_id;
  SELECT evidence INTO STRICT p FROM tv_episode_upgrade_phase WHERE operation_id=NEW.operation_id AND action='prepare';
  INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,total_bytes)
  VALUES(gen_random_uuid(),a.operation_id,a.client_id,a.candidate_id,p->>'info_hash','tracking',p->>'save_path',(p->>'total')::bigint);
 ELSIF NEW.action='organize' AND NEW.result->>'status'='imported_pending_retirement' THEN
  UPDATE download_task SET state='completed',progress=1,downloaded_bytes=total_bytes,organization_state='completed',finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE operation_id=NEW.operation_id;
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION pulse_tv_upgrade_control_physical() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.backend_start IS DISTINCT FROM pulse_tv_upgrade_backend_start() OR NOT pulse_download_control_dedicated_lock(OLD.backend_pid,OLD.advisory_key)
   OR EXISTS(SELECT 1 FROM download_control_authority a WHERE a.physical_token=OLD.token AND NOT EXISTS(SELECT 1 FROM download_control_return r WHERE r.command_id=a.command_id)) THEN
   RAISE EXCEPTION 'authentic TV control physical return required' USING ERRCODE='23514';
  END IF;
  RETURN OLD;
 END IF;
 IF NEW.advisory_key<>pulse_tv_upgrade_lock(NEW.operation_id) OR NEW.backend_start IS DISTINCT FROM pulse_tv_upgrade_backend_start() OR NOT pulse_download_control_dedicated_lock(NEW.backend_pid,NEW.advisory_key)
 OR NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_authority a JOIN download_task t USING(operation_id) JOIN tv_episode_upgrade_phase s ON s.operation_id=a.operation_id AND s.action='send' JOIN tv_episode_upgrade_return r ON r.operation_id=s.operation_id AND r.action=s.action WHERE a.operation_id=NEW.operation_id AND t.state='tracking' AND s.result->>'status'='tracking' AND r.status='confirmed')
 OR EXISTS(SELECT 1 FROM tv_episode_upgrade_phase WHERE operation_id=NEW.operation_id AND (action IN ('organization-confirm','organize','retirement-confirm','retire') OR (action LIKE 'observe:%' AND result->>'status'='organization_review_required')))
 OR EXISTS(SELECT 1 FROM tv_episode_upgrade_permit p LEFT JOIN tv_episode_upgrade_return r USING(operation_id,action) WHERE p.operation_id=NEW.operation_id AND r.operation_id IS NULL) THEN
  RAISE EXCEPTION 'TV control physical binding rejected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_tv_upgrade_control_gate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM download_control_physical WHERE operation_id=NEW.operation_id) OR EXISTS(SELECT 1 FROM download_control_command c WHERE c.operation_id=NEW.operation_id AND NOT EXISTS(SELECT 1 FROM download_control_confirmation f WHERE f.command_id=c.id)) THEN RAISE EXCEPTION 'TV control result must be observed before another phase' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER tv_episode_upgrade_no_generic_task ON download_task;
CREATE TRIGGER tv_episode_upgrade_no_generic_task BEFORE INSERT OR UPDATE OR DELETE ON download_task FOR EACH ROW EXECUTE FUNCTION pulse_tv_episode_upgrade_no_generic_task();
CREATE TRIGGER tv_upgrade_control_task AFTER INSERT ON tv_episode_upgrade_phase FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_control_task();
DROP TRIGGER download_control_physical_binding ON download_control_physical;
CREATE TRIGGER download_control_physical_binding_insert BEFORE INSERT ON download_control_physical FOR EACH ROW WHEN (NEW.scope<>'tv-episode-upgrade') EXECUTE FUNCTION pulse_download_control_physical_binding();
CREATE TRIGGER download_control_physical_binding_delete BEFORE DELETE ON download_control_physical FOR EACH ROW WHEN (OLD.scope<>'tv-episode-upgrade') EXECUTE FUNCTION pulse_download_control_physical_binding();
CREATE TRIGGER tv_upgrade_control_physical_insert BEFORE INSERT ON download_control_physical FOR EACH ROW WHEN (NEW.scope='tv-episode-upgrade') EXECUTE FUNCTION pulse_tv_upgrade_control_physical();
CREATE TRIGGER tv_upgrade_control_physical_delete BEFORE DELETE ON download_control_physical FOR EACH ROW WHEN (OLD.scope='tv-episode-upgrade') EXECUTE FUNCTION pulse_tv_upgrade_control_physical();
CREATE TRIGGER download_control_dedicated_gate BEFORE INSERT ON tv_episode_upgrade_permit FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_control_gate();
CREATE TRIGGER download_control_dedicated_gate BEFORE INSERT ON tv_episode_upgrade_phase FOR EACH ROW EXECUTE FUNCTION pulse_tv_upgrade_control_gate();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE download_control_authority,download_control_physical,download_task,tv_episode_upgrade_phase IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM download_control_authority WHERE scope='tv-episode-upgrade') OR EXISTS(SELECT 1 FROM download_control_physical WHERE scope='tv-episode-upgrade') OR EXISTS(SELECT 1 FROM download_task t JOIN tv_episode_upgrade_authority a USING(operation_id)) THEN RAISE EXCEPTION 'TV control history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER download_control_dedicated_gate ON tv_episode_upgrade_permit;
DROP TRIGGER download_control_dedicated_gate ON tv_episode_upgrade_phase;
DROP TRIGGER tv_upgrade_control_physical_insert ON download_control_physical;
DROP TRIGGER tv_upgrade_control_physical_delete ON download_control_physical;
DROP TRIGGER download_control_physical_binding_insert ON download_control_physical;
DROP TRIGGER download_control_physical_binding_delete ON download_control_physical;
CREATE TRIGGER download_control_physical_binding BEFORE INSERT OR DELETE ON download_control_physical FOR EACH ROW EXECUTE FUNCTION pulse_download_control_physical_binding();
DROP TRIGGER tv_upgrade_control_task ON tv_episode_upgrade_phase;
DROP FUNCTION pulse_tv_upgrade_control_task();
DROP FUNCTION pulse_tv_upgrade_control_physical();
DROP FUNCTION pulse_tv_upgrade_control_gate();
DROP TRIGGER tv_episode_upgrade_no_generic_task ON download_task;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_tv_episode_upgrade_no_generic_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM tv_episode_upgrade_authority WHERE operation_id=NEW.operation_id) THEN RAISE EXCEPTION 'TV episode upgrade requires dedicated authority'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER tv_episode_upgrade_no_generic_task BEFORE INSERT OR UPDATE ON download_task FOR EACH ROW EXECUTE FUNCTION pulse_tv_episode_upgrade_no_generic_task();
ALTER TABLE download_control_physical DROP CONSTRAINT download_control_physical_scope_check;
ALTER TABLE download_control_physical ADD CONSTRAINT download_control_physical_scope_check CHECK(scope IN ('automatic-tv','complete-tv','movie-upgrade'));
ALTER TABLE download_control_authority DROP CONSTRAINT download_control_authority_scope_check;
ALTER TABLE download_control_authority ADD CONSTRAINT download_control_authority_scope_check CHECK(scope IN ('automatic-tv','complete-tv','movie-upgrade'));
