-- All reads belong to a READ ONLY REPEATABLE READ status snapshot.
-- name: GetSubscriptionStatusTime :one
SELECT transaction_timestamp()::timestamptz AS observed_at;

-- name: ListSubscriptionStatusUnits :many
SELECT w.id, w.unit_id, w.is_skipped, u.unit_key, u.air_date, u.retired, u.facts_version,u.metadata_version
FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id
WHERE w.subscription_id=$1 ORDER BY u.unit_key;

-- name: ListSubscriptionStatusFiles :many
SELECT w.id AS wanted_id, f.rel_path, uf.stage, uf.quality_profile
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
JOIN library_file_unit fu ON fu.unit_id=w.unit_id AND fu.retired_at IS NULL
JOIN library_file f ON f.id=fu.file_id AND f.root_id=s.root_id AND f.retired_at IS NULL
LEFT JOIN unit_fact uf ON uf.unit_id=w.unit_id AND uf.source_file_id=f.id AND uf.root_id=s.root_id
 AND uf.stage='imported' AND uf.revoked_at IS NULL
 AND (uf.expires_at IS NULL OR uf.expires_at>sqlc.arg(observed_at)::timestamptz)
WHERE w.subscription_id=$1 ORDER BY w.id, f.id, uf.created_at DESC;

-- Expired observation/reservation TTL never frees an unreleased physical acquisition.
-- name: ListSubscriptionStatusReservations :many
SELECT w.id AS wanted_id, COALESCE(d.state,'unknown')::text AS task_state
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
JOIN unit_reservation r ON r.unit_id=w.unit_id AND r.root_id=s.root_id AND r.state IN ('reserved','dispatched')
LEFT JOIN download_task d ON d.operation_id=r.operation_id
WHERE w.subscription_id=$1
UNION ALL
SELECT w.id,CASE WHEN a.legacy_conflict OR a.source_stamp='' OR c.source_config_version<>i.config_version OR c.source_config_version=0 OR i.health<>'online' OR d.state IN ('submitting','unknown','failed','canceled') THEN 'global_unknown' ELSE 'global_available' END::text
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id JOIN acquisition_reservation a ON a.unit_id=w.unit_id AND a.released_at IS NULL
LEFT JOIN download_task d ON d.operation_id=a.operation_id
JOIN release_candidate c ON c.id=a.candidate_id JOIN indexer i ON i.id=c.indexer_id
WHERE w.subscription_id=$1 AND NOT EXISTS(SELECT 1 FROM unit_reservation r WHERE r.unit_id=w.unit_id AND r.root_id=s.root_id AND r.operation_id=a.operation_id AND r.state IN ('reserved','dispatched'));

-- name: HasSubscriptionStatusLocalReuseSchema :one
SELECT (to_regclass('library_reuse_unit') IS NOT NULL AND to_regclass('library_reuse_plan') IS NOT NULL)::boolean AS installed;

-- name: ListSubscriptionStatusLocalReuse :many
SELECT w.id AS wanted_id, CASE WHEN p.state IN ('waiting','review_required') THEN 'unknown' ELSE 'local_reuse' END::text AS task_state
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
JOIN library_reuse_unit r ON r.unit_id=w.unit_id AND r.root_id=s.root_id AND r.active
JOIN library_reuse_plan p ON p.id=r.plan_id WHERE w.subscription_id=$1;

-- name: ListSubscriptionStatusTaskFacts :many
SELECT w.id AS wanted_id, uf.stage FROM wanted_item w
JOIN subscription s ON s.id=w.subscription_id
JOIN unit_fact uf ON uf.unit_id=w.unit_id AND uf.root_id=s.root_id AND uf.revoked_at IS NULL
 AND uf.stage IN ('submitted','downloaded') AND uf.expires_at>sqlc.arg(observed_at)::timestamptz
JOIN download_task d ON d.id=uf.source_task_id
WHERE w.subscription_id=$1 AND EXISTS(SELECT 1 FROM unit_reservation r WHERE r.unit_id=w.unit_id AND r.root_id=s.root_id AND r.operation_id=d.operation_id AND r.state IN ('reserved','dispatched'))
ORDER BY uf.created_at DESC;

-- name: ListSubscriptionStatusRounds :many
SELECT DISTINCT ON (wanted_item_id) wanted_item_id, state, abort_reason, snapshot_intent_version, snapshot_policy_revision, deadline
FROM search_round WHERE subscription_id=$1 ORDER BY wanted_item_id, created_at DESC, id DESC;

-- name: HasSubscriptionStatusDownloader :one
SELECT EXISTS(SELECT 1 FROM download_client WHERE sqlc.narg(downloader_id)::uuid IS NULL OR id=sqlc.narg(downloader_id)::uuid)::boolean AS available;
