-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION pulse_notification_event_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM notification_batch WHERE event_id=OLD.id) OR EXISTS(SELECT 1 FROM notification_batch_member WHERE source_event_id=OLD.id) THEN RAISE EXCEPTION 'notification source and sealed event evidence is immutable';END IF;
 IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_event_evidence_guard BEFORE UPDATE OR DELETE ON outbox_event FOR EACH ROW EXECUTE FUNCTION pulse_notification_event_evidence_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_notification_member_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM outbox_event e JOIN library_file f ON f.id=NEW.file_id
 WHERE e.id=NEW.source_event_id AND e.event_type='media.imported' AND e.aggregate_type='media' AND e.aggregate_id=NEW.unit_id::text
 AND e.payload->>'unit_id'=NEW.unit_id::text AND e.payload->>'file_id'=NEW.file_id::text AND f.root_id=NEW.root_id) THEN RAISE EXCEPTION 'notification member source mismatch';END IF;RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_member_source_guard BEFORE INSERT ON notification_batch_member FOR EACH ROW EXECUTE FUNCTION pulse_notification_member_source_guard();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM notification_batch) THEN RAISE EXCEPTION 'notification evidence guards cannot be removed with history';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER notification_member_source_guard ON notification_batch_member;
DROP FUNCTION pulse_notification_member_source_guard();
DROP TRIGGER notification_event_evidence_guard ON outbox_event;
DROP FUNCTION pulse_notification_event_evidence_guard();
