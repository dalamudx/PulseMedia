-- +goose Up
-- No master key is available to SQL migrations. Quarantine plaintext legacy
-- channel configuration rather than fabricating ciphertext or auto-sending it.
ALTER TABLE notification_channel ADD COLUMN config_encrypted BYTEA;
UPDATE notification_channel SET config='{}',is_enabled=false,updated_at=clock_timestamp();
ALTER TABLE notification_channel ADD CONSTRAINT notification_config_no_plaintext CHECK (config='{}'::jsonb);

-- Historical generic recipients do not identify the intended server/channel.
-- Do not expand them to today's recipients or report them as delivered.
UPDATE outbox_delivery SET state='dead',lease_owner=NULL,lease_until=NULL,lease_token=lease_token+1,
 error='legacy recipient unspecified; audited review required',updated_at=clock_timestamp()
WHERE consumer IN ('notify','mediaserver.refresh','scheduler','download_manager') AND state!='delivered';
CREATE INDEX outbox_expired_delivery_idx ON outbox_delivery(consumer,lease_until) WHERE state='delivering';

-- +goose Down
DROP INDEX outbox_expired_delivery_idx;
ALTER TABLE notification_channel DROP CONSTRAINT notification_config_no_plaintext;
ALTER TABLE notification_channel DROP COLUMN config_encrypted;
-- Removed secrets and recipient evidence cannot be reconstructed on downgrade.
