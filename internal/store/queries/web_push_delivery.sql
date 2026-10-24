-- name: ListImportedWebPushDevices :many
SELECT d.id,d.user_id,d.vapid_version
FROM web_push_device d JOIN app_user a ON a.id=d.user_id AND a.is_active
JOIN library_file f ON f.id=sqlc.arg(file_id) AND f.retired_at IS NULL
JOIN library_file_unit fu ON fu.file_id=f.id AND fu.unit_id=sqlc.arg(unit_id) AND fu.retired_at IS NULL
JOIN media_unit u ON u.id=fu.unit_id AND NOT u.retired
WHERE d.status='active' AND d.vapid_version=sqlc.arg(vapid_version)
AND EXISTS(SELECT 1 FROM unit_fact fact WHERE fact.source_file_id=f.id AND fact.unit_id=u.id AND fact.root_id=f.root_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp()))
AND (a.role='admin' OR EXISTS(SELECT 1 FROM subscription s JOIN wanted_item w ON w.subscription_id=s.id
 WHERE s.user_id=a.id AND s.media_id=u.media_id AND s.root_id=f.root_id AND s.state<>'archived'
 AND w.unit_id=u.id AND w.intent_version=s.intent_version AND NOT w.is_skipped AND NOT w.scope_excluded))
ORDER BY d.id;

-- name: InsertWebPushDeliveryBinding :exec
INSERT INTO web_push_delivery_binding(delivery_id,device_id,user_id,vapid_version,unit_id,file_id,root_id)
VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: LoadWebPushDeliveryBinding :one
SELECT b.*,d.subscription_encrypted FROM web_push_delivery_binding b JOIN web_push_device d ON d.id=b.device_id AND d.user_id=b.user_id AND d.vapid_version=b.vapid_version
WHERE b.delivery_id=$1;

-- name: GuardWebPushDeliverySource :one
SELECT d.id
FROM web_push_delivery_binding b JOIN web_push_device d ON d.id=b.device_id AND d.user_id=b.user_id AND d.vapid_version=b.vapid_version
JOIN app_user a ON a.id=b.user_id AND a.is_active
JOIN library_file f ON f.id=b.file_id AND f.root_id=b.root_id AND f.retired_at IS NULL
JOIN library_file_unit fu ON fu.file_id=f.id AND fu.unit_id=b.unit_id AND fu.retired_at IS NULL
JOIN media_unit u ON u.id=b.unit_id AND NOT u.retired
WHERE b.delivery_id=$1 AND d.status='active' AND b.vapid_version=$2
AND EXISTS(SELECT 1 FROM unit_fact fact WHERE fact.source_file_id=f.id AND fact.unit_id=u.id AND fact.root_id=f.root_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp()))
AND (a.role='admin' OR EXISTS(SELECT 1 FROM subscription s JOIN wanted_item w ON w.subscription_id=s.id
 WHERE s.user_id=a.id AND s.media_id=u.media_id AND s.root_id=f.root_id AND s.state<>'archived'
 AND w.unit_id=u.id AND w.intent_version=s.intent_version AND NOT w.is_skipped AND NOT w.scope_excluded))
FOR SHARE OF d,a,f,fu,u;

-- name: TryWebPushPhysicalLock :one
SELECT pg_try_advisory_lock(hashtextextended('webpush.device:'||sqlc.arg(device_id)::text,0));

-- name: ReleaseWebPushPhysicalLock :one
SELECT pg_advisory_unlock(hashtextextended('webpush.device:'||sqlc.arg(device_id)::text,0));

-- name: ExpireWebPushDevice :exec
UPDATE web_push_device SET status='expired',updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 AND status='active';

-- name: SkipOwnedWebPushDelivery :one
UPDATE outbox_delivery SET state='skipped',skipped_at=clock_timestamp(),skip_reason='recipient_retired',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE id=$1 AND consumer=$2 AND state='delivering' AND lease_owner=$3 AND lease_token=$4 AND lease_until>clock_timestamp()
RETURNING id;
