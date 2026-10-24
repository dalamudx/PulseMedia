-- name: ListDuePersonFollowConfigs :many
SELECT c.id,c.owner_id FROM person_follow_config c JOIN app_user u ON u.id=c.owner_id
JOIN policy p ON p.id=c.quality_policy_id AND p.current_revision=c.quality_policy_revision
WHERE c.enabled AND u.is_active AND u.role IN ('admin','member')
AND (c.movie_root_id IS NULL OR EXISTS(SELECT 1 FROM library_root WHERE id=c.movie_root_id AND media_type='movie' AND health='online'))
AND (c.tv_root_id IS NULL OR EXISTS(SELECT 1 FROM library_root WHERE id=c.tv_root_id AND media_type='tv' AND health='online'))
AND NOT EXISTS(SELECT 1 FROM person_follow_indexer b LEFT JOIN indexer_lifecycle l ON l.indexer_id=b.indexer_id WHERE b.config_id=c.id AND (NOT coalesce(l.enabled,true) OR l.deleted_at IS NOT NULL))
AND NOT EXISTS(SELECT 1 FROM person_follow_sync_run r WHERE r.config_id=c.id AND r.status IN ('queued','running','review_required'))
AND NOT EXISTS(SELECT 1 FROM person_follow_sync_run r WHERE r.config_id=c.id AND r.config_version=c.version AND r.finished_at>clock_timestamp()-make_interval(mins=>c.interval_minutes))
ORDER BY c.owner_id,c.id LIMIT 64;

-- name: PersonFollowConfigDue :one
SELECT NOT EXISTS(SELECT 1 FROM person_follow_sync_run r WHERE r.config_id=sqlc.arg(config_id)::uuid AND
(r.status IN ('queued','running','review_required') OR (r.config_version=sqlc.arg(config_version)::bigint AND r.finished_at>clock_timestamp()-make_interval(mins=>sqlc.arg(interval_minutes)::int))));

-- name: ReadPersonFollowVersion :one
SELECT * FROM person_follow_config_history WHERE config_id=$1 AND version=$2;

-- name: InsertPersonFollowRun :exec
INSERT INTO person_follow_sync_run(id,config_id,config_version,owner_id,config_snapshot,status) VALUES($1,$2,$3,$4,$5,'queued');

-- name: InsertPersonFollowSyncJob :exec
INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,'person_follow_sync','queued',$2,3);

-- name: InsertPersonFollowSyncPage :exec
INSERT INTO person_follow_sync_page(job_id,run_id,page_start)VALUES($1,$2,$3);
