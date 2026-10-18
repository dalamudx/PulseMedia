-- +goose Up
CREATE TABLE notification_batch (
 id UUID PRIMARY KEY, media_id UUID NOT NULL REFERENCES media(id), channel_id UUID NOT NULL REFERENCES notification_channel(id),
 state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','ready','sealed')),
 deadline TIMESTAMPTZ NOT NULL, member_count INTEGER NOT NULL DEFAULT 0 CHECK(member_count BETWEEN 0 AND 32),
 event_id UUID UNIQUE REFERENCES outbox_event(id), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,channel_id), CHECK((state='sealed')=(event_id IS NOT NULL))
);
CREATE UNIQUE INDEX notification_batch_open ON notification_batch(media_id,channel_id) WHERE state='open';
CREATE INDEX notification_batch_due ON notification_batch(deadline,id) WHERE state<>'sealed';
CREATE TABLE notification_batch_member (
 batch_id UUID NOT NULL, channel_id UUID NOT NULL, source_event_id UUID NOT NULL REFERENCES outbox_event(id),
 unit_id UUID NOT NULL REFERENCES media_unit(id), root_id UUID NOT NULL REFERENCES library_root(id), file_id UUID NOT NULL REFERENCES library_file(id),
 PRIMARY KEY(batch_id,source_event_id), UNIQUE(channel_id,source_event_id), FOREIGN KEY(batch_id,channel_id) REFERENCES notification_batch(id,channel_id)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_notification_batch_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.state='sealed' OR NEW.id<>OLD.id OR NEW.media_id<>OLD.media_id OR NEW.channel_id<>OLD.channel_id OR NEW.deadline<>OLD.deadline OR NEW.created_at<>OLD.created_at OR NEW.member_count<OLD.member_count OR (OLD.state='ready' AND NEW.state='open') THEN
 RAISE EXCEPTION 'notification batch evidence is immutable'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_batch_guard BEFORE UPDATE OR DELETE ON notification_batch FOR EACH ROW EXECUTE FUNCTION pulse_notification_batch_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_notification_member_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b notification_batch;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'notification membership is immutable'; END IF;
 SELECT * INTO b FROM notification_batch WHERE id=NEW.batch_id FOR NO KEY UPDATE;
 IF b.state<>'open' OR b.member_count>=32 OR NOT EXISTS(SELECT 1 FROM media_unit WHERE id=NEW.unit_id AND media_id=b.media_id) THEN RAISE EXCEPTION 'notification batch is closed or inconsistent'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER notification_member_guard BEFORE INSERT OR UPDATE OR DELETE ON notification_batch_member FOR EACH ROW EXECUTE FUNCTION pulse_notification_member_guard();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM notification_batch) THEN RAISE EXCEPTION 'notification batch history cannot be discarded'; END IF;END$$;
-- +goose StatementEnd
DROP TABLE notification_batch_member;
DROP TABLE notification_batch;
DROP FUNCTION pulse_notification_member_guard();
DROP FUNCTION pulse_notification_batch_guard();
