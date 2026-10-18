-- +goose Up
-- Runtime content evidence does not alter frozen transfer-step JSON or any
-- historical digest. A successor may explicitly inherit the same source proof.
CREATE TABLE transfer_content_evidence (
 step_id UUID PRIMARY KEY REFERENCES transfer_step(id) ON DELETE RESTRICT,
 content JSONB NOT NULL CHECK(jsonb_typeof(content)='object' AND content ? 'File' AND content ? 'SHA256' AND octet_length(content::text)<=2048),
 inherited_from UUID REFERENCES transfer_content_evidence(step_id) ON DELETE RESTRICT,
 frozen_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION guard_verified_transfer_content() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'verified transfer content is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER verified_transfer_content_frozen BEFORE UPDATE OR DELETE ON transfer_content_evidence
FOR EACH ROW EXECUTE FUNCTION guard_verified_transfer_content();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION guard_verified_transfer_step() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.target_evidence->'file_action' IS NULL THEN
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'verified transfer history is immutable' USING ERRCODE='23514';
 END IF;
 IF ROW(NEW.id,NEW.plan_id,NEW.unit_id,NEW.source_path,NEW.target_path,NEW.action,NEW.step_order,
        NEW.source_size,NEW.source_mtime,NEW.source_root,NEW.source_rel,NEW.target_rel,
        NEW.source_device,NEW.source_inode,NEW.quality_profile,NEW.created_at,NEW.target_evidence)
    IS DISTINCT FROM
    ROW(OLD.id,OLD.plan_id,OLD.unit_id,OLD.source_path,OLD.target_path,OLD.action,OLD.step_order,
        OLD.source_size,OLD.source_mtime,OLD.source_root,OLD.source_rel,OLD.target_rel,
        OLD.source_device,OLD.source_inode,OLD.quality_profile,OLD.created_at,OLD.target_evidence) THEN
  RAISE EXCEPTION 'verified transfer approval is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER verified_transfer_step_frozen BEFORE UPDATE OR DELETE ON transfer_step
FOR EACH ROW EXECUTE FUNCTION guard_verified_transfer_step();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION guard_verified_transfer_plan() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM transfer_step s WHERE s.plan_id=OLD.id AND s.target_evidence->'file_action' IS NOT NULL) THEN
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'verified transfer history is immutable' USING ERRCODE='23514';
 END IF;
 IF ROW(NEW.id,NEW.operation_id,NEW.root_id,NEW.requires_approval,NEW.strategy,NEW.root_path,
        NEW.root_generation,NEW.root_device,NEW.root_inode,NEW.old_file_id,NEW.created_at)
    IS DISTINCT FROM
    ROW(OLD.id,OLD.operation_id,OLD.root_id,OLD.requires_approval,OLD.strategy,OLD.root_path,
        OLD.root_generation,OLD.root_device,OLD.root_inode,OLD.old_file_id,OLD.created_at) THEN
  RAISE EXCEPTION 'verified transfer plan is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER verified_transfer_plan_frozen BEFORE UPDATE OR DELETE ON transfer_plan
FOR EACH ROW EXECUTE FUNCTION guard_verified_transfer_plan();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM transfer_step WHERE target_evidence->'file_action' IS NOT NULL)
 OR EXISTS(SELECT 1 FROM transfer_content_evidence) THEN
  RAISE EXCEPTION 'verified transfer history prevents downgrade' USING ERRCODE='23514';
 END IF;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER verified_transfer_step_frozen ON transfer_step;
DROP TRIGGER verified_transfer_plan_frozen ON transfer_plan;
DROP FUNCTION guard_verified_transfer_step();
DROP FUNCTION guard_verified_transfer_plan();
DROP TRIGGER verified_transfer_content_frozen ON transfer_content_evidence;
DROP FUNCTION guard_verified_transfer_content();
DROP TABLE transfer_content_evidence;
