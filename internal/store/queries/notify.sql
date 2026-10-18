-- Notification Queries

-- name: CreateNotificationChannel :one
INSERT INTO notification_channel (
    id, name, channel_type, config, config_encrypted, is_enabled, created_at, updated_at
) VALUES (
    $1, $2, $3, '{}', $4, true, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetNotificationChannelByID :one
SELECT * FROM notification_channel WHERE id = $1;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channel ORDER BY created_at,id;

-- name: LockNotificationChannel :one
SELECT * FROM notification_channel WHERE id=$1 FOR NO KEY UPDATE;
-- name: SetNotificationChannelState :one
UPDATE notification_channel SET is_enabled=$2,updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 AND updated_at=$3 AND (NOT $2::boolean OR replaced_by IS NULL) RETURNING *;

-- name: DisableNotificationChannel :one
UPDATE notification_channel SET is_enabled=false,updated_at=clock_timestamp() WHERE id=$1 RETURNING *;

-- name: ListActiveNotificationChannels :many
SELECT * FROM notification_channel
WHERE is_enabled = true
ORDER BY created_at ASC;

-- name: ReplaceNotificationChannel :one
UPDATE notification_channel SET is_enabled=false,replaced_by=$3,updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
WHERE id=$1 AND updated_at=$2 AND replaced_by IS NULL RETURNING *;

