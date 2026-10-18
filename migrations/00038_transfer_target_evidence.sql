-- +goose Up
ALTER TABLE transfer_step ADD COLUMN target_evidence JSONB CHECK(target_evidence IS NULL OR jsonb_typeof(target_evidence)='object');
-- Historical plans have no canonical target-version freeze. Do not fabricate one.
UPDATE transfer_plan SET state='review_required',updated_at=clock_timestamp()
WHERE state IN ('draft','awaiting_approval','queued','approved');
-- Executing plans retain their physical exclusion; the writeback guard rejects missing evidence.
-- +goose StatementBegin
CREATE FUNCTION pulse_transfer_target_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF OLD.target_evidence IS NOT NULL AND (NEW.target_evidence IS DISTINCT FROM OLD.target_evidence OR NEW.unit_id IS DISTINCT FROM OLD.unit_id OR NEW.plan_id IS DISTINCT FROM OLD.plan_id) THEN RAISE EXCEPTION 'frozen transfer target cannot be rewritten';END IF;
 IF (OLD.target_evidence IS NULL AND NEW.target_evidence IS NOT NULL) OR (NEW.state='completed' AND OLD.state<>'completed') THEN
 IF NEW.target_evidence IS NULL THEN RAISE EXCEPTION 'missing transfer target evidence';END IF;
  IF NOT EXISTS(SELECT 1 FROM media_unit u JOIN media m ON m.id=u.media_id WHERE u.id=NEW.unit_id AND u.id=(NEW.target_evidence->>'unit_id')::uuid AND u.media_id=(NEW.target_evidence->>'media_id')::uuid AND u.unit_key='movie' AND NEW.target_evidence->>'unit_key'=u.unit_key AND NOT u.retired AND m.media_type='movie' AND u.metadata_version=(NEW.target_evidence->>'unit_version')::bigint AND m.metadata_version=(NEW.target_evidence->>'media_version')::bigint AND NEW.target_evidence->>'parser_version' ~ '^episode[.]v[0-9]+$') THEN RAISE EXCEPTION 'transfer target evidence does not match canonical input';END IF;
 END IF;RETURN NEW;END$$;
-- +goose StatementEnd
CREATE TRIGGER transfer_target_evidence_guard BEFORE UPDATE ON transfer_step FOR EACH ROW EXECUTE FUNCTION pulse_transfer_target_evidence_guard();

-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM transfer_step WHERE target_evidence IS NOT NULL) THEN RAISE EXCEPTION 'transfer target evidence cannot be discarded';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER transfer_target_evidence_guard ON transfer_step;
DROP FUNCTION pulse_transfer_target_evidence_guard();
ALTER TABLE transfer_step DROP COLUMN target_evidence;
