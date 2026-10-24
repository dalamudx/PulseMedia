-- +goose Up
-- Keep historical destinations immutable; new channel types share existing delivery contracts.
ALTER TABLE notification_channel DROP CONSTRAINT notification_channel_channel_type_check;
ALTER TABLE notification_channel ADD CONSTRAINT notification_channel_channel_type_check CHECK(channel_type IN ('webhook','telegram','wechat','dingtalk','feishu','bark'));

-- +goose Down
LOCK TABLE notification_channel IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM notification_channel WHERE channel_type IN ('wechat','dingtalk','feishu','bark')) THEN
  RAISE EXCEPTION 'robot channel history cannot be discarded';
 END IF;
END$$;
-- +goose StatementEnd
ALTER TABLE notification_channel DROP CONSTRAINT notification_channel_channel_type_check;
ALTER TABLE notification_channel ADD CONSTRAINT notification_channel_channel_type_check CHECK(channel_type IN ('webhook','telegram'));
