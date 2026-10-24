-- name: LockOutboxDelivery :one
SELECT * FROM outbox_delivery WHERE id=$1 FOR UPDATE;

-- name: GuardOutboxDelivery :one
SELECT * FROM outbox_delivery WHERE id=sqlc.arg(id) AND consumer=sqlc.arg(consumer)
 AND state='delivering' AND lease_owner=sqlc.arg(owner) AND lease_token=sqlc.arg(token)
 AND lease_until > clock_timestamp();

-- name: GetOutboxEventByID :one
SELECT * FROM outbox_event WHERE id=$1;

-- name: ListRuntimeOutboxConsumers :many
SELECT DISTINCT consumer FROM outbox_delivery
WHERE state IN ('pending','delivering')
 AND (consumer LIKE 'notify:%' OR consumer LIKE 'mediaserver.refresh:%' OR consumer LIKE 'webpush:%')
ORDER BY consumer;

-- name: RecoverExpiredOutboxDeliveries :execrows
UPDATE outbox_delivery SET state=CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'pending' END,
 lease_token=lease_token+1, lease_owner=NULL, lease_until=NULL,
 error='delivery interrupted; at-least-once retry required', not_before=clock_timestamp(), updated_at=clock_timestamp()
WHERE consumer=$1 AND state='delivering' AND lease_until <= clock_timestamp();

-- name: ReturnDisabledNotificationDelivery :one
UPDATE outbox_delivery d SET state='pending',attempts=GREATEST(attempts-1,0),lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE d.id=sqlc.arg(id) AND d.consumer=sqlc.arg(consumer) AND d.state='delivering' AND d.lease_owner=sqlc.arg(owner) AND d.lease_token=sqlc.arg(token) AND d.lease_until>clock_timestamp()
AND EXISTS(SELECT 1 FROM notification_channel n WHERE d.consumer='notify:'||n.id::text AND NOT n.is_enabled)
RETURNING d.*;

