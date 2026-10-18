-- +goose Up
-- M3-07: Notification Channels, Webhooks & Telegram Integration

CREATE TABLE IF NOT EXISTS notification_channel (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    channel_type TEXT NOT NULL CHECK (channel_type IN ('webhook', 'telegram')),
    config JSONB NOT NULL,
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- +goose Down
DROP TABLE IF EXISTS notification_channel;
