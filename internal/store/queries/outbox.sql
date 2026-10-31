-- name: InsertOutboxEvent :one
INSERT INTO outbox_event (
    id, aggregate_type, aggregate_id, event_type, payload, seq, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, statement_timestamp()
) RETURNING *;

-- name: LockOutboxAggregate :exec
SELECT pg_advisory_xact_lock(hashtextextended(json_build_array(sqlc.arg(aggregate_type)::text, sqlc.arg(aggregate_id)::text)::text, 0));

-- name: GetNextAggregateSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint + 1 AS next_seq
FROM outbox_event
WHERE aggregate_type = $1 AND aggregate_id = $2;

-- name: InsertOutboxDelivery :one
INSERT INTO outbox_delivery (
    id, event_id, consumer, aggregate_type, aggregate_id, seq, state,
    attempts, max_attempts, not_before, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, 'pending', 0, $7, statement_timestamp(), statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: ClaimOutboxDelivery :one
WITH candidate AS (
    SELECT d.id FROM outbox_delivery d
    WHERE d.consumer = $1::text
      AND d.state = 'pending'
      AND NOT EXISTS (SELECT 1 FROM notification_channel n WHERE d.consumer='notify:'||n.id::text AND NOT n.is_enabled)
      AND d.not_before <= statement_timestamp()
      AND d.attempts < d.max_attempts
      -- A new media-level notification must not bypass an unresolved historical unit-level notification.
      AND NOT (d.aggregate_type='notification.media' AND EXISTS (
        SELECT 1 FROM outbox_delivery legacy JOIN media_unit u ON legacy.aggregate_type='media' AND legacy.aggregate_id=u.id::text
        WHERE u.media_id::text=d.aggregate_id AND legacy.consumer=d.consumer AND legacy.state NOT IN ('delivered','skipped')
      ))
      -- Ensure NO uncompleted preceding deliveries for this same aggregate
      AND NOT EXISTS (
        SELECT 1 FROM outbox_delivery prev
        WHERE prev.consumer = d.consumer
          AND prev.aggregate_type = d.aggregate_type
          AND prev.aggregate_id = d.aggregate_id
          AND prev.seq < d.seq
          AND prev.state NOT IN ('delivered','skipped')
      )
    ORDER BY d.seq ASC, d.id ASC
    LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE outbox_delivery AS d
SET state = 'delivering', lease_owner = $2::text,
    lease_until = clock_timestamp() + ($3::integer * interval '1 millisecond'),
    lease_token = d.lease_token + 1, attempts = d.attempts + 1,
    updated_at = clock_timestamp()
FROM candidate AS c
WHERE d.id = c.id AND d.state = 'pending' AND $3::integer > 0
RETURNING d.*;

-- name: FinishOutboxDelivery :one
UPDATE outbox_delivery
SET state = 'delivered', delivered_at = statement_timestamp(),
    updated_at = statement_timestamp(), lease_owner = NULL, lease_until = NULL
WHERE id = $1::uuid AND state = 'delivering'
  AND lease_owner = $2::text AND lease_token = $3::bigint
  AND lease_until > clock_timestamp()
RETURNING *;

-- name: FailOutboxDelivery :one
UPDATE outbox_delivery
SET state = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'pending' END,
    error = $4::text,
    not_before = clock_timestamp() + ($5::integer * interval '1 millisecond'),
    lease_owner = NULL, lease_until = NULL,
    updated_at = statement_timestamp()
WHERE id = $1::uuid AND state = 'delivering'
  AND lease_owner = $2::text AND lease_token = $3::bigint
  AND lease_until > clock_timestamp() AND $5::integer > 0
RETURNING *;

-- name: GetOutboxDeliveryByID :one
SELECT * FROM outbox_delivery WHERE id = $1;
