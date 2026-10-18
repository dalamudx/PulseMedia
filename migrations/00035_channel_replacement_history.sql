-- +goose Up
-- consumer is historically a text reference; an FK on the successor alone cannot protect the old row.
-- +goose StatementBegin
CREATE FUNCTION pulse_channel_replacement_delete_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF OLD.replaced_by IS NOT NULL OR EXISTS(SELECT 1 FROM notification_channel WHERE replaced_by=OLD.id) THEN RAISE EXCEPTION 'channel replacement identities cannot be deleted';END IF;RETURN OLD;END$$;
-- +goose StatementEnd
CREATE TRIGGER channel_replacement_delete_guard BEFORE DELETE ON notification_channel FOR EACH ROW EXECUTE FUNCTION pulse_channel_replacement_delete_guard();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM notification_channel WHERE replaced_by IS NOT NULL) THEN RAISE EXCEPTION 'channel replacement identity guard cannot be weakened with history';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER channel_replacement_delete_guard ON notification_channel;
DROP FUNCTION pulse_channel_replacement_delete_guard();
