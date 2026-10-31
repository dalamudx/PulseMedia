-- name: HasMovieDiscoverySchema :one
SELECT (to_regclass('subscription_movie_discovery_run') IS NOT NULL)::boolean AS installed;

-- name: ReadMovieDiscoverySearch :one
SELECT COALESCE((SELECT to_jsonb(d) FROM subscription_movie_discovery_run d WHERE search_id=$1),'{}'::jsonb)::jsonb AS body;

-- name: ListDueMovieDiscoveries :many
SELECT s.id FROM subscription s JOIN media m ON m.id=s.media_id JOIN app_user a ON a.id=s.user_id LEFT JOIN subscription_movie_discovery d ON d.subscription_id=s.id
WHERE s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND m.media_type='movie' AND a.is_active AND a.role='admin' AND (d.next_discovery_at IS NULL OR d.next_discovery_at<=clock_timestamp())
ORDER BY d.next_discovery_at NULLS FIRST,s.id LIMIT sqlc.arg(row_limit)::integer;

-- name: EnsureMovieDiscovery :exec
INSERT INTO subscription_movie_discovery(subscription_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: LockMovieDiscoveryDue :one
SELECT (next_discovery_at<=clock_timestamp())::boolean AS due FROM subscription_movie_discovery WHERE subscription_id=$1 FOR UPDATE;

-- name: AdvanceMovieDiscovery :exec
UPDATE subscription_movie_discovery SET next_discovery_at=clock_timestamp()+interval '6 hours' WHERE subscription_id=$1;

-- name: ListMovieDiscoverySources :many
SELECT i.id,i.config_version FROM indexer i LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id WHERE i.id=ANY(sqlc.arg(indexer_ids)::uuid[]) AND COALESCE(l.enabled,true) AND l.deleted_at IS NULL ORDER BY i.id FOR SHARE OF i;

-- name: CountLiveMovieDiscoverySearches :one
SELECT count(*) FROM interactive_search WHERE actor_id=$1 AND state='running' AND cancelled_at IS NULL AND deadline>clock_timestamp();

-- name: CreateMovieDiscoverySearch :one
INSERT INTO interactive_search(id,actor_id,actor_role,query,media_id,subscription_id,ranking_policy,source_count,created_at,deadline)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(actor_id)::uuid,sqlc.arg(actor_role)::text,sqlc.arg(query)::text,sqlc.arg(media_id)::uuid,sqlc.arg(subscription_id)::uuid,sqlc.arg(ranking_policy)::jsonb,sqlc.arg(source_count)::integer,statement_timestamp(),statement_timestamp()+interval '30 seconds')
RETURNING to_jsonb(interactive_search)::jsonb AS body;

-- name: CreateMovieDiscoveryJob :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts) VALUES(sqlc.arg(id)::uuid,'interactive_search_source',sqlc.arg(dedupe_key)::text,'queued',sqlc.arg(payload)::jsonb,1);

-- name: CreateMovieDiscoverySource :exec
INSERT INTO interactive_search_source(search_id,indexer_id,config_version,job_id) VALUES($1,$2,$3,$4);

-- name: CreateMovieDiscoveryRun :exec
INSERT INTO subscription_movie_discovery_run(search_id,subscription_id,input_stamp) VALUES($1,$2,$3);

-- name: SetMovieDiscoverySearch :exec
UPDATE subscription_movie_discovery SET search_id=$2 WHERE subscription_id=$1;

-- name: ReadMovieDiscoverySchedule :one
SELECT search_id,next_discovery_at FROM subscription_movie_discovery WHERE subscription_id=$1;

-- name: ReadMovieDiscoveryState :one
SELECT (CASE WHEN deadline<=clock_timestamp() AND state='running' THEN 'expired' ELSE state END)::text AS state FROM interactive_search WHERE id=$1;
