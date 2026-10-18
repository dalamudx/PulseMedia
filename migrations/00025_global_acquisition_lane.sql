-- +goose Up
CREATE TABLE acquisition_reservation (
 id UUID PRIMARY KEY,unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
 acquisition_lane TEXT NOT NULL DEFAULT 'default' CHECK(acquisition_lane='default'),
 operation_id UUID NOT NULL,candidate_id UUID NOT NULL REFERENCES release_candidate(id) ON DELETE RESTRICT,
 task_id UUID REFERENCES download_task(id) ON DELETE RESTRICT,
 source_root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
 source_generation BIGINT NOT NULL,source_stamp TEXT NOT NULL,
 legacy_conflict BOOLEAN NOT NULL DEFAULT false,released_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX acquisition_reservation_active ON acquisition_reservation(unit_id,acquisition_lane) WHERE released_at IS NULL;
CREATE INDEX acquisition_reservation_operation ON acquisition_reservation(operation_id);
-- Preserve all old root protections. One default lane quarantines ambiguous
-- legacy physical operations; it does not pretend their artifacts were merged.
INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp,legacy_conflict)
SELECT chosen.id,chosen.unit_id,chosen.operation_id,chosen.candidate_id,t.id,chosen.root_id,r.generation,'',
 (SELECT count(DISTINCT old.operation_id)>1 FROM unit_reservation old WHERE old.unit_id=chosen.unit_id AND old.state IN ('reserved','dispatched'))
FROM (SELECT DISTINCT ON(unit_id) * FROM unit_reservation WHERE state IN ('reserved','dispatched') ORDER BY unit_id,created_at,id)chosen
JOIN library_root r ON r.id=chosen.root_id LEFT JOIN download_task t ON t.operation_id=chosen.operation_id;
UPDATE job j SET state='dead',error='legacy acquisition lane needs manual review',lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp()
WHERE j.kind='download_submission' AND j.state IN ('queued','running') AND EXISTS(
 SELECT 1 FROM unit_reservation old JOIN acquisition_reservation lane ON lane.unit_id=old.unit_id AND lane.legacy_conflict
 WHERE old.operation_id::text=j.payload->>'operation_id');
-- +goose StatementBegin
CREATE FUNCTION pulse_acquisition_lane_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND ROW(NEW.unit_id,NEW.acquisition_lane,NEW.operation_id,NEW.candidate_id,NEW.source_root_id,NEW.source_generation,NEW.source_stamp) IS DISTINCT FROM ROW(OLD.unit_id,OLD.acquisition_lane,OLD.operation_id,OLD.candidate_id,OLD.source_root_id,OLD.source_generation,OLD.source_stamp) THEN RAISE EXCEPTION 'physical acquisition source is immutable'; END IF;
 IF NEW.task_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM download_task t WHERE t.id=NEW.task_id AND t.operation_id=NEW.operation_id AND t.candidate_id=NEW.candidate_id) THEN RAISE EXCEPTION 'physical task identity mismatch'; END IF;
 IF TG_OP='UPDATE' AND OLD.task_id IS NOT NULL AND NEW.task_id IS DISTINCT FROM OLD.task_id THEN RAISE EXCEPTION 'physical task binding is immutable'; END IF;
 RETURN NEW; END $$;
-- +goose StatementEnd
CREATE TRIGGER acquisition_lane_guard BEFORE INSERT OR UPDATE ON acquisition_reservation FOR EACH ROW EXECUTE FUNCTION pulse_acquisition_lane_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_acquisition_lane_revision() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE target UUID; BEGIN
 IF TG_OP='INSERT' THEN FOR target IN SELECT DISTINCT unit_id FROM new_rows ORDER BY unit_id LOOP UPDATE media_unit SET facts_version=facts_version+1 WHERE id=target;END LOOP;
 ELSIF TG_OP='DELETE' THEN FOR target IN SELECT DISTINCT unit_id FROM old_rows ORDER BY unit_id LOOP UPDATE media_unit SET facts_version=facts_version+1 WHERE id=target;END LOOP;
 ELSE FOR target IN SELECT DISTINCT unit_id FROM (SELECT unit_id FROM old_rows UNION SELECT unit_id FROM new_rows)x ORDER BY unit_id LOOP UPDATE media_unit SET facts_version=facts_version+1 WHERE id=target;END LOOP; END IF;RETURN NULL; END $$;
-- +goose StatementEnd
CREATE TRIGGER acquisition_lane_facts_insert AFTER INSERT ON acquisition_reservation REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_acquisition_lane_revision();
CREATE TRIGGER acquisition_lane_facts_update AFTER UPDATE ON acquisition_reservation REFERENCING NEW TABLE AS new_rows OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_acquisition_lane_revision();
CREATE TRIGGER acquisition_lane_facts_delete AFTER DELETE ON acquisition_reservation REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_acquisition_lane_revision();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM acquisition_reservation WHERE released_at IS NULL) THEN RAISE EXCEPTION 'cannot downgrade live global acquisition protection'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE acquisition_reservation;
DROP FUNCTION pulse_acquisition_lane_guard();DROP FUNCTION pulse_acquisition_lane_revision();
