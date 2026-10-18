-- name: GetInteractiveSearchContinuationDelay :one
SELECT GREATEST(0,ceil(extract(epoch FROM (GREATEST(next_query_at,COALESCE(rate_limit_until,next_query_at))-clock_timestamp()))*1000))::bigint AS delay_ms
FROM indexer WHERE id=$1 AND config_version=$2;
-- name: InteractiveSearchReadHeader :one
SELECT to_jsonb(h)::jsonb AS body FROM interactive_search h WHERE id=$1;
-- name: InteractiveSearchLockHeader :one
SELECT to_jsonb(h)::jsonb AS body FROM interactive_search h WHERE id=$1 FOR UPDATE;
-- name: InteractiveSearchLockAccount :one
SELECT role,is_active FROM app_user WHERE id=$1 FOR SHARE;
-- name: InteractiveSearchLockSubscription :one
SELECT user_id,media_id FROM subscription WHERE id=$1 FOR SHARE;
-- name: InteractiveSearchLockMedia :one
SELECT id FROM media WHERE id=$1 FOR SHARE;
-- name: InteractiveSearchLockSource :one
SELECT to_jsonb(s)::jsonb AS body FROM interactive_search_source s WHERE search_id=$1 AND indexer_id=$2 FOR UPDATE;
-- name: InteractiveSearchLockJob :one
SELECT to_jsonb(j)::jsonb AS body FROM job j WHERE id=$1 FOR UPDATE;
-- name: InteractiveSearchSourceCurrent :one
SELECT EXISTS(SELECT 1 FROM indexer i LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id
 WHERE i.id=$1 AND i.config_version=$2 AND COALESCE(l.enabled,true) AND l.deleted_at IS NULL) AS current;
-- name: InteractiveSearchJobValid :one
SELECT EXISTS(SELECT 1 FROM job j WHERE j.id=sqlc.arg(job_id)::uuid AND j.kind='interactive_search_source'
 AND j.state='running' AND j.lease_owner=sqlc.arg(owner)::text AND j.lease_token=sqlc.arg(token)::bigint AND j.lease_until>clock_timestamp()
 AND j.dedupe_key=sqlc.arg(search_id)::uuid::text||':'||sqlc.arg(indexer_id)::uuid::text
 AND j.payload=jsonb_build_object('search_id',sqlc.arg(search_id)::uuid,'indexer_id',sqlc.arg(indexer_id)::uuid,'config_version',sqlc.arg(config_version)::bigint)
 AND clock_timestamp()<sqlc.arg(deadline)::timestamptz) AS valid;
-- name: InteractiveSearchLinkQuery :execrows
INSERT INTO interactive_search_query(search_id,indexer_id,page,query_id,source_job_id,source_token)
SELECT sqlc.arg(search_id)::uuid,sqlc.arg(indexer_id)::uuid,sqlc.arg(page)::integer,q.id,sqlc.arg(job_id)::uuid,sqlc.arg(token)::bigint FROM indexer_query q
WHERE q.id=sqlc.arg(query_id)::uuid AND q.indexer_id=sqlc.arg(indexer_id)::uuid AND q.page=sqlc.arg(page)::integer AND q.query_type='interactive' AND q.state='running'
 AND q.keyword=sqlc.arg(keyword)::text AND q.config_version=sqlc.arg(config_version)::bigint AND q.rss_job_id IS NULL
 AND sqlc.arg(page)::integer=(SELECT count(*) FROM interactive_search_query a WHERE a.search_id=sqlc.arg(search_id)::uuid AND a.indexer_id=sqlc.arg(indexer_id)::uuid);

-- name: LockFreeInteractiveQuery :one
SELECT * FROM indexer_query WHERE id=$1;
