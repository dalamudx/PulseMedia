-- name: ListManagedIntegrationDeliveries :many
SELECT * FROM outbox_delivery WHERE (consumer LIKE 'notify:%' OR consumer LIKE 'mediaserver.refresh:%') AND (NOT sqlc.arg(has_event_id)::boolean OR event_id=sqlc.arg(event_id)::uuid) AND (sqlc.arg(state)::text='' OR state=sqlc.arg(state)::text) AND (NOT sqlc.arg(has_cursor)::boolean OR (updated_at,id)<(sqlc.arg(after_updated_at)::timestamptz,sqlc.arg(after_id)::uuid)) ORDER BY updated_at DESC,id DESC LIMIT sqlc.arg(page_limit)::integer;
-- name: ReplayDeadIntegrationDelivery :one
UPDATE outbox_delivery SET state='pending',attempts=0,lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,not_before=clock_timestamp(),error=NULL,updated_at=clock_timestamp()
WHERE id=$1 AND state='dead' AND lease_owner IS NULL AND lease_until IS NULL AND lease_token=$2 AND attempts=$3 AND updated_at=$4 RETURNING *;

-- name: SkipDeadIntegrationDelivery :one
UPDATE outbox_delivery SET state='skipped',lease_token=lease_token+1,skipped_at=clock_timestamp(),skip_reason=$5,error=NULL,updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
WHERE id=$1 AND state='dead' AND lease_owner IS NULL AND lease_until IS NULL AND delivered_at IS NULL AND lease_token=$2 AND attempts=$3 AND updated_at=$4 RETURNING *;

