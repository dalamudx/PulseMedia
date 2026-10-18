-- +goose Up
-- Local demands have their own origin; they never fabricate a release or download.
CREATE TABLE library_reuse_plan (
 id UUID PRIMARY KEY,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
 source_file_id UUID NOT NULL REFERENCES library_file(id) ON DELETE RESTRICT,
 job_id UUID NOT NULL UNIQUE REFERENCES job(id) ON DELETE RESTRICT,
 action TEXT NOT NULL CHECK(action IN ('hardlink','copy','reflink')),
 target_rel TEXT NOT NULL,
 frozen JSONB NOT NULL CHECK(jsonb_typeof(frozen)='object'),
 reason TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','waiting','running','completed','review_required')),
 target_file_id UUID REFERENCES library_file(id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK((state='completed')=(target_file_id IS NOT NULL)),
 UNIQUE(id,root_id)
);
CREATE TABLE library_reuse_unit (
 plan_id UUID NOT NULL,
 root_id UUID NOT NULL,
 unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
 active BOOLEAN NOT NULL DEFAULT true,
 PRIMARY KEY(plan_id,unit_id),
 FOREIGN KEY(plan_id,root_id) REFERENCES library_reuse_plan(id,root_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX library_reuse_unit_active ON library_reuse_unit(root_id,unit_id) WHERE active;
CREATE TABLE library_reuse_content (
 plan_id UUID PRIMARY KEY REFERENCES library_reuse_plan(id) ON DELETE RESTRICT,
 content JSONB NOT NULL CHECK(jsonb_typeof(content)='object' AND content ? 'File' AND content ? 'SHA256'),
 frozen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION guard_library_reuse_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR TG_TABLE_NAME='library_reuse_content' THEN
  RAISE EXCEPTION 'local reuse history is immutable' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='library_reuse_plan' THEN
  IF ROW(NEW.id,NEW.actor_id,NEW.root_id,NEW.source_file_id,NEW.job_id,NEW.action,NEW.target_rel,NEW.frozen,NEW.created_at)
   IS DISTINCT FROM ROW(OLD.id,OLD.actor_id,OLD.root_id,OLD.source_file_id,OLD.job_id,OLD.action,OLD.target_rel,OLD.frozen,OLD.created_at)
   OR OLD.state='completed' AND NEW IS DISTINCT FROM OLD THEN
   RAISE EXCEPTION 'local reuse approval is immutable' USING ERRCODE='23514';
  END IF;
 ELSE
  IF ROW(NEW.plan_id,NEW.root_id,NEW.unit_id) IS DISTINCT FROM ROW(OLD.plan_id,OLD.root_id,OLD.unit_id)
   OR NOT OLD.active AND NEW.active
   OR NOT NEW.active AND NOT EXISTS(SELECT 1 FROM library_reuse_plan WHERE id=NEW.plan_id AND state='completed') THEN
   RAISE EXCEPTION 'local reuse protection requires completed import' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER library_reuse_plan_history BEFORE UPDATE OR DELETE ON library_reuse_plan FOR EACH ROW EXECUTE FUNCTION guard_library_reuse_history();
CREATE TRIGGER library_reuse_unit_history BEFORE UPDATE OR DELETE ON library_reuse_unit FOR EACH ROW EXECUTE FUNCTION guard_library_reuse_history();
CREATE TRIGGER library_reuse_content_history BEFORE UPDATE OR DELETE ON library_reuse_content FOR EACH ROW EXECUTE FUNCTION guard_library_reuse_history();
-- Both sources serialize on the same unit row and check the other reservation kind.
CREATE FUNCTION guard_library_reuse_exclusion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='unit_reservation' THEN
  IF NEW.state NOT IN ('reserved','dispatched') THEN RETURN NEW; END IF;
 END IF;
 PERFORM 1 FROM media_unit WHERE id=NEW.unit_id FOR NO KEY UPDATE;
 IF TG_TABLE_NAME='library_reuse_unit' THEN
  IF NEW.active AND EXISTS(SELECT 1 FROM unit_reservation WHERE root_id=NEW.root_id AND unit_id=NEW.unit_id AND state IN ('reserved','dispatched')) THEN
   RAISE EXCEPTION 'target already reserved' USING ERRCODE='23505';
  END IF;
 ELSE
  IF NEW.state IN ('reserved','dispatched') AND EXISTS(SELECT 1 FROM library_reuse_unit WHERE root_id=NEW.root_id AND unit_id=NEW.unit_id AND active) THEN
   RAISE EXCEPTION 'target reserved for local reuse' USING ERRCODE='23505';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER library_reuse_exclusion BEFORE INSERT OR UPDATE ON library_reuse_unit FOR EACH ROW EXECUTE FUNCTION guard_library_reuse_exclusion();
CREATE TRIGGER download_reuse_exclusion BEFORE INSERT OR UPDATE ON unit_reservation FOR EACH ROW EXECUTE FUNCTION guard_library_reuse_exclusion();
-- +goose StatementEnd

CREATE TRIGGER library_reuse_revision_insert AFTER INSERT ON library_reuse_unit REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_acquisition_lane_revision();
CREATE TRIGGER library_reuse_revision_update AFTER UPDATE ON library_reuse_unit REFERENCING NEW TABLE AS new_rows OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_acquisition_lane_revision();

-- +goose StatementBegin
CREATE FUNCTION pulse_library_reuse_plan_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target UUID;
BEGIN
 FOR target IN SELECT DISTINCT u.unit_id FROM library_reuse_unit u
 JOIN new_rows n ON n.id=u.plan_id JOIN old_rows o ON o.id=n.id
 WHERE n.state IS DISTINCT FROM o.state ORDER BY u.unit_id LOOP
  UPDATE media_unit SET facts_version=facts_version+1 WHERE id=target;
 END LOOP;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER library_reuse_plan_revision AFTER UPDATE ON library_reuse_plan REFERENCING NEW TABLE AS new_rows OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_library_reuse_plan_revision();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM library_reuse_plan) THEN RAISE EXCEPTION 'local reuse history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER download_reuse_exclusion ON unit_reservation;
DROP TABLE library_reuse_content,library_reuse_unit,library_reuse_plan;
DROP FUNCTION guard_library_reuse_exclusion();
DROP FUNCTION guard_library_reuse_history();
DROP FUNCTION pulse_library_reuse_plan_revision();
