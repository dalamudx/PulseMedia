-- +goose Up
ALTER TABLE notification_channel ADD COLUMN replaced_by UUID UNIQUE REFERENCES notification_channel(id);
ALTER TABLE notification_channel ADD CONSTRAINT channel_replacement_distinct CHECK(replaced_by IS NULL OR replaced_by<>id);
ALTER TABLE notification_channel ADD CONSTRAINT channel_replacement_disabled CHECK(replaced_by IS NULL OR NOT is_enabled);
-- +goose StatementBegin
CREATE FUNCTION pulse_channel_replacement_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.config IS DISTINCT FROM OLD.config OR NEW.config_encrypted IS DISTINCT FROM OLD.config_encrypted OR NEW.channel_type IS DISTINCT FROM OLD.channel_type THEN RAISE EXCEPTION 'channel destination and credentials require replacement';END IF;
 IF OLD.replaced_by IS NOT NULL AND NEW.replaced_by IS DISTINCT FROM OLD.replaced_by THEN RAISE EXCEPTION 'channel replacement history is immutable';END IF;
 RETURN NEW;
END$$;
-- +goose StatementEnd
CREATE TRIGGER channel_replacement_guard BEFORE UPDATE ON notification_channel FOR EACH ROW EXECUTE FUNCTION pulse_channel_replacement_guard();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM notification_channel WHERE replaced_by IS NOT NULL) THEN RAISE EXCEPTION 'channel replacement history cannot be discarded';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER channel_replacement_guard ON notification_channel;
DROP FUNCTION pulse_channel_replacement_guard();
ALTER TABLE notification_channel DROP CONSTRAINT channel_replacement_disabled,DROP CONSTRAINT channel_replacement_distinct,DROP COLUMN replaced_by;
