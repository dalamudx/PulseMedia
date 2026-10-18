-- +goose Up
-- Source transactions advance input revisions only. They NEVER lock wanted/subscriptions.
ALTER TABLE unit_fact ADD COLUMN revocation_reason TEXT;
ALTER TABLE media_unit ADD COLUMN metadata_version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE library_root ADD COLUMN health_revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE wanted_item
 ADD COLUMN evaluated_intent_version BIGINT,
 ADD COLUMN evaluated_policy_id TEXT,
 ADD COLUMN evaluated_policy_revision BIGINT,
 ADD COLUMN evaluated_root_id UUID,
 ADD COLUMN evaluated_unit_id UUID,
 ADD COLUMN evaluated_media_id UUID,
 ADD COLUMN evaluated_root_generation BIGINT,
 ADD COLUMN evaluated_health_revision BIGINT,
 ADD COLUMN evaluated_media_metadata_version BIGINT,
 ADD COLUMN evaluated_unit_metadata_version BIGINT,
 ADD COLUMN evaluated_facts_version BIGINT,
 ADD COLUMN evaluated_state TEXT,
 ADD COLUMN evaluated_skipped BOOLEAN,
 ADD COLUMN projection_valid_until TIMESTAMPTZ;

-- Existing caches have no provenance; NULL stamps remain invalid, never backfilled as current.
UPDATE search_round SET state='aborted',abort_reason='input_version_contract_changed',finished_at=clock_timestamp() WHERE state='running';

-- +goose StatementBegin
CREATE FUNCTION pulse_metadata_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME IN ('media','media_unit') THEN IF NEW.metadata_version<OLD.metadata_version THEN RAISE EXCEPTION 'metadata revision must not decrease'; END IF; END IF;
 IF TG_TABLE_NAME='media' THEN
  IF ROW(NEW.title,NEW.original_title,NEW.year,NEW.media_type) IS DISTINCT FROM ROW(OLD.title,OLD.original_title,OLD.year,OLD.media_type) THEN NEW.metadata_version:=GREATEST(OLD.metadata_version+1,NEW.metadata_version); END IF;
 ELSIF TG_TABLE_NAME='media_unit' THEN
  IF NEW.facts_version<OLD.facts_version THEN RAISE EXCEPTION 'facts revision must not decrease'; END IF;
  IF ROW(NEW.media_id,NEW.unit_key,NEW.air_date,NEW.retired,NEW.season_number,NEW.episode_number,NEW.absolute_number) IS DISTINCT FROM ROW(OLD.media_id,OLD.unit_key,OLD.air_date,OLD.retired,OLD.season_number,OLD.episode_number,OLD.absolute_number) THEN NEW.metadata_version:=GREATEST(OLD.metadata_version+1,NEW.metadata_version); END IF;
 ELSE
  IF NEW.health_revision<OLD.health_revision OR NEW.generation<OLD.generation THEN RAISE EXCEPTION 'root revision must not decrease'; END IF;
  IF ROW(NEW.path,NEW.media_type,NEW.generation) IS DISTINCT FROM ROW(OLD.path,OLD.media_type,OLD.generation) THEN NEW.baseline_completed:=false; END IF;
  IF ROW(NEW.path,NEW.media_type,NEW.health,NEW.generation,NEW.baseline_completed) IS DISTINCT FROM ROW(OLD.path,OLD.media_type,OLD.health,OLD.generation,OLD.baseline_completed) THEN NEW.health_revision:=GREATEST(OLD.health_revision+1,NEW.health_revision); END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER media_metadata_revision BEFORE UPDATE ON media FOR EACH ROW EXECUTE FUNCTION pulse_metadata_revision();
CREATE TRIGGER unit_metadata_revision BEFORE UPDATE ON media_unit FOR EACH ROW EXECUTE FUNCTION pulse_metadata_revision();
CREATE TRIGGER root_health_revision BEFORE UPDATE ON library_root FOR EACH ROW EXECUTE FUNCTION pulse_metadata_revision();

-- Statement transition tables let bulk updates lock ALL affected units in UUID order.
-- Parent business locks use NO KEY UPDATE: compatible with FK KEY SHARE, exclusive for attribute writes.
-- +goose StatementBegin
CREATE FUNCTION pulse_facts_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE units UUID[]; objects UUID[]; target UUID;
BEGIN
 IF TG_TABLE_NAME IN ('unit_fact','library_file_unit','unit_reservation') THEN
  IF TG_OP='INSERT' THEN SELECT array_agg(DISTINCT unit_id) INTO units FROM new_rows;
  ELSIF TG_OP='DELETE' THEN SELECT array_agg(DISTINCT unit_id) INTO units FROM old_rows;
  ELSE SELECT array_agg(DISTINCT unit_id) INTO units FROM (SELECT unit_id FROM old_rows UNION SELECT unit_id FROM new_rows) x; END IF;
 ELSIF TG_TABLE_NAME='library_file' THEN
  IF TG_OP='INSERT' THEN SELECT array_agg(id) INTO objects FROM new_rows;
  ELSIF TG_OP='DELETE' THEN SELECT array_agg(id) INTO objects FROM old_rows;
  ELSE SELECT array_agg(n.id) INTO objects FROM new_rows n JOIN old_rows o USING(id) WHERE ROW(n.root_id,n.rel_path,n.size_bytes,n.mtime_ns,n.file_fingerprint,n.retired_at) IS DISTINCT FROM ROW(o.root_id,o.rel_path,o.size_bytes,o.mtime_ns,o.file_fingerprint,o.retired_at); END IF;
  IF TG_OP='UPDATE' THEN
   UPDATE unit_fact SET revoked_at=clock_timestamp(),revocation_reason='file_content_changed' WHERE revoked_at IS NULL AND stage IN ('imported','confirmed') AND source_file_id IN (SELECT n.id FROM new_rows n JOIN old_rows o USING(id) WHERE ROW(n.size_bytes,n.mtime_ns,n.file_fingerprint) IS DISTINCT FROM ROW(o.size_bytes,o.mtime_ns,o.file_fingerprint));
  END IF;
  SELECT array_agg(DISTINCT unit_id) INTO units FROM library_file_unit WHERE file_id=ANY(objects);
 ELSIF TG_TABLE_NAME='download_task' THEN
  IF TG_OP='INSERT' THEN SELECT array_agg(operation_id) INTO objects FROM new_rows;
  ELSIF TG_OP='DELETE' THEN SELECT array_agg(operation_id) INTO objects FROM old_rows;
  ELSE SELECT array_agg(n.operation_id) INTO objects FROM new_rows n JOIN old_rows o USING(id) WHERE ROW(n.state,n.operation_id) IS DISTINCT FROM ROW(o.state,o.operation_id); END IF;
  SELECT array_agg(DISTINCT unit_id) INTO units FROM unit_reservation WHERE operation_id=ANY(objects);
 ELSIF TG_TABLE_NAME='search_round' THEN
  IF TG_OP='INSERT' THEN SELECT array_agg(wanted_item_id) INTO objects FROM new_rows;
  ELSIF TG_OP='DELETE' THEN SELECT array_agg(wanted_item_id) INTO objects FROM old_rows;
  ELSE SELECT array_agg(n.wanted_item_id) INTO objects FROM new_rows n JOIN old_rows o USING(id) WHERE ROW(n.state,n.abort_reason,n.deadline) IS DISTINCT FROM ROW(o.state,o.abort_reason,o.deadline); END IF;
  SELECT array_agg(DISTINCT unit_id) INTO units FROM wanted_item WHERE id=ANY(objects);
 END IF;
 FOR target IN SELECT DISTINCT x FROM unnest(units) x ORDER BY x LOOP
  UPDATE media_unit SET facts_version=facts_version+1 WHERE id=target;
 END LOOP;
 RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ DECLARE name TEXT; BEGIN
 FOREACH name IN ARRAY ARRAY['unit_fact','library_file_unit','unit_reservation','library_file','download_task','search_round'] LOOP
  EXECUTE format('CREATE TRIGGER %I AFTER INSERT ON %I REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_facts_revision()',name||'_facts_insert',name);
  EXECUTE format('CREATE TRIGGER %I AFTER UPDATE ON %I REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_facts_revision()',name||'_facts_update',name);
  EXECUTE format('CREATE TRIGGER %I AFTER DELETE ON %I REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_facts_revision()',name||'_facts_delete',name);
 END LOOP;
END $$;
-- +goose StatementEnd

-- Metadata identities affect matching without changing title/year.
-- +goose StatementBegin
CREATE FUNCTION pulse_identity_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target UUID; ids UUID[];
BEGIN
 IF TG_OP='INSERT' THEN SELECT array_agg(DISTINCT media_id) INTO ids FROM new_rows;
 ELSIF TG_OP='DELETE' THEN SELECT array_agg(DISTINCT media_id) INTO ids FROM old_rows;
 ELSE SELECT array_agg(DISTINCT media_id) INTO ids FROM (SELECT media_id FROM old_rows UNION SELECT media_id FROM new_rows)x; END IF;
 FOR target IN SELECT DISTINCT x FROM unnest(ids)x ORDER BY x LOOP UPDATE media SET metadata_version=metadata_version+1 WHERE id=target; END LOOP;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER identity_revision_insert AFTER INSERT ON media_identity REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_identity_revision();
CREATE TRIGGER identity_revision_update AFTER UPDATE ON media_identity REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_identity_revision();
CREATE TRIGGER identity_revision_delete AFTER DELETE ON media_identity REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_identity_revision();

CREATE VIEW wanted_projection_validity AS
SELECT w.id,
 (w.evaluated_intent_version IS NOT DISTINCT FROM s.intent_version
 AND w.evaluated_policy_id IS NOT DISTINCT FROM s.quality_policy_id
 AND w.evaluated_policy_revision IS NOT DISTINCT FROM s.quality_policy_revision
 AND w.evaluated_unit_id IS NOT DISTINCT FROM w.unit_id
 AND w.evaluated_media_id IS NOT DISTINCT FROM s.media_id
 AND u.media_id=s.media_id
 AND w.evaluated_root_id IS NOT DISTINCT FROM s.root_id
 AND w.evaluated_root_generation IS NOT DISTINCT FROM r.generation
 AND w.evaluated_health_revision IS NOT DISTINCT FROM r.health_revision
 AND w.evaluated_media_metadata_version IS NOT DISTINCT FROM m.metadata_version
 AND w.evaluated_unit_metadata_version IS NOT DISTINCT FROM u.metadata_version
 AND w.evaluated_facts_version IS NOT DISTINCT FROM u.facts_version
 AND w.evaluated_state IS NOT DISTINCT FROM s.state
 AND w.evaluated_skipped IS NOT DISTINCT FROM w.is_skipped) AS inputs_current
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
JOIN library_root r ON r.id=s.root_id JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.id=w.unit_id;

-- +goose Down
DROP VIEW wanted_projection_validity;
DROP TRIGGER identity_revision_insert ON media_identity;
DROP TRIGGER identity_revision_update ON media_identity;
DROP TRIGGER identity_revision_delete ON media_identity;
DROP FUNCTION pulse_identity_revision();
-- +goose StatementBegin
DO $$ DECLARE name TEXT; BEGIN FOREACH name IN ARRAY ARRAY['unit_fact','library_file_unit','unit_reservation','library_file','download_task','search_round'] LOOP
 EXECUTE format('DROP TRIGGER %I ON %I',name||'_facts_insert',name);
 EXECUTE format('DROP TRIGGER %I ON %I',name||'_facts_update',name);
 EXECUTE format('DROP TRIGGER %I ON %I',name||'_facts_delete',name);
END LOOP; END $$;
-- +goose StatementEnd
DROP FUNCTION pulse_facts_revision();
DROP TRIGGER media_metadata_revision ON media;
DROP TRIGGER unit_metadata_revision ON media_unit;
DROP TRIGGER root_health_revision ON library_root;
DROP FUNCTION pulse_metadata_revision();
ALTER TABLE wanted_item DROP COLUMN evaluated_intent_version,DROP COLUMN evaluated_policy_id,DROP COLUMN evaluated_policy_revision,DROP COLUMN evaluated_root_id,DROP COLUMN evaluated_unit_id,DROP COLUMN evaluated_media_id,DROP COLUMN evaluated_root_generation,DROP COLUMN evaluated_health_revision,DROP COLUMN evaluated_media_metadata_version,DROP COLUMN evaluated_unit_metadata_version,DROP COLUMN evaluated_facts_version,DROP COLUMN evaluated_state,DROP COLUMN evaluated_skipped,DROP COLUMN projection_valid_until;
ALTER TABLE unit_fact DROP COLUMN revocation_reason;
ALTER TABLE library_root DROP COLUMN health_revision;
ALTER TABLE media_unit DROP COLUMN metadata_version;
