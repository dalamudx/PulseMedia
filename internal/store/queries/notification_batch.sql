-- name: GetImportedNotificationInput :one
SELECT u.media_id,u.id AS unit_id,f.id AS file_id,f.root_id FROM library_file f
JOIN library_file_unit fu ON fu.file_id=f.id AND fu.retired_at IS NULL
JOIN media_unit u ON u.id=fu.unit_id AND NOT u.retired
WHERE f.id=sqlc.arg(file_id) AND u.id=sqlc.arg(unit_id) AND f.retired_at IS NULL
AND EXISTS(SELECT 1 FROM unit_fact fact WHERE fact.unit_id=u.id AND fact.source_file_id=f.id AND fact.root_id=f.root_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND(fact.expires_at IS NULL OR fact.expires_at>clock_timestamp()));
-- name: LockOpenNotificationBatch :one
SELECT * FROM notification_batch WHERE media_id=$1 AND channel_id=$2 AND state='open' FOR NO KEY UPDATE;
-- name: CreateNotificationBatch :one
INSERT INTO notification_batch(id,media_id,channel_id,deadline) VALUES($1,$2,$3,clock_timestamp()+interval '3 seconds') RETURNING *;
-- name: AppendNotificationMember :exec
INSERT INTO notification_batch_member(batch_id,channel_id,source_event_id,unit_id,root_id,file_id) VALUES($1,$2,$3,$4,$5,$6);
-- name: AdvanceNotificationBatch :exec
UPDATE notification_batch SET member_count=member_count+1,state=CASE WHEN member_count+1>=32 THEN 'ready' ELSE 'open' END WHERE id=$1 AND state='open';
-- name: ReadyNotificationBatch :exec
UPDATE notification_batch SET state='ready' WHERE id=$1 AND state='open';
-- name: NotificationBatchExpired :one
SELECT deadline<=clock_timestamp() FROM notification_batch WHERE id=$1;
-- name: LockDueNotificationBatch :one
SELECT * FROM notification_batch b WHERE state<>'sealed' AND NOT(id=ANY(sqlc.arg(excluded)::uuid[])) AND(state='ready' OR deadline<=clock_timestamp())
AND NOT EXISTS(SELECT 1 FROM notification_batch prev WHERE prev.channel_id=b.channel_id AND prev.media_id=b.media_id AND prev.state<>'sealed' AND(prev.created_at,prev.id)<(b.created_at,b.id))
ORDER BY created_at,id LIMIT 1 FOR NO KEY UPDATE SKIP LOCKED;
-- name: ListNotificationBatchMembers :many
SELECT * FROM notification_batch_member WHERE batch_id=$1 ORDER BY source_event_id;
-- name: SealNotificationBatch :exec
UPDATE notification_batch SET state='sealed',event_id=$2 WHERE id=$1 AND state<>'sealed';

-- name: ListManagedNotificationBatches :many
SELECT b.id,b.media_id,b.channel_id,b.event_id,b.deadline,b.member_count,n.name AS recipient_name,n.is_enabled AS recipient_enabled,
CASE WHEN b.state='sealed' THEN COALESCE(d.state,'unavailable') WHEN b.state='ready' OR b.deadline<=clock_timestamp() THEN 'sealing_pending' ELSE 'collecting' END::text AS observed_state,
(SELECT count(DISTINCT unit_id) FROM notification_batch_member WHERE batch_id=b.id)::bigint AS unit_count,
(SELECT count(DISTINCT file_id) FROM notification_batch_member WHERE batch_id=b.id)::bigint AS file_count,
(SELECT count(DISTINCT root_id) FROM notification_batch_member WHERE batch_id=b.id)::bigint AS root_count
FROM notification_batch b JOIN notification_channel n ON n.id=b.channel_id
LEFT JOIN outbox_delivery d ON d.event_id=b.event_id AND d.consumer='notify:'||b.channel_id::text
ORDER BY b.created_at DESC,b.id DESC LIMIT 100;

