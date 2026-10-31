-- name: HasTVEpisodeDiscoverySchema :one
SELECT (to_regclass('subscription_tv_episode_discovery_run') IS NOT NULL)::boolean AS installed;

-- name: ReadTVEpisodeDiscoverySearch :one
SELECT COALESCE((SELECT to_jsonb(d) FROM subscription_tv_episode_discovery_run d WHERE search_id=$1),'{}'::jsonb)::jsonb AS body;

-- name: ListDueTVEpisodeDiscoveries :many
SELECT s.id AS subscription_id,u.id AS unit_id FROM subscription s JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.media_id=m.id JOIN app_user a ON a.id=s.user_id LEFT JOIN subscription_tv_episode_discovery d ON d.subscription_id=s.id AND d.unit_id=u.id
WHERE s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND m.media_type='tv' AND a.is_active AND a.role='admin' AND NOT u.retired AND u.air_date IS NOT NULL AND u.air_date<=CURRENT_DATE AND u.season_number>0 AND u.episode_number>0 AND u.absolute_number IS NULL AND (d.next_discovery_at IS NULL OR d.next_discovery_at<=clock_timestamp())
ORDER BY d.next_discovery_at NULLS FIRST,s.id,u.id LIMIT sqlc.arg(row_limit)::integer;

-- name: EnsureTVEpisodeDiscovery :exec
INSERT INTO subscription_tv_episode_discovery(subscription_id,unit_id) VALUES($1,$2) ON CONFLICT DO NOTHING;

-- name: LockTVEpisodeDiscoveryDue :one
SELECT (next_discovery_at<=clock_timestamp())::boolean AS due FROM subscription_tv_episode_discovery WHERE subscription_id=$1 AND unit_id=$2 FOR UPDATE;

-- name: AdvanceTVEpisodeDiscovery :exec
UPDATE subscription_tv_episode_discovery SET next_discovery_at=clock_timestamp()+interval '6 hours' WHERE subscription_id=$1 AND unit_id=$2;

-- name: CreateTVEpisodeDiscoveryRun :exec
INSERT INTO subscription_tv_episode_discovery_run(search_id,subscription_id,unit_id,input_stamp) VALUES($1,$2,$3,$4);

-- name: SetTVEpisodeDiscoverySearch :exec
UPDATE subscription_tv_episode_discovery SET search_id=$3 WHERE subscription_id=$1 AND unit_id=$2;

-- name: ReadTVEpisodeDiscoverySchedule :one
SELECT search_id,next_discovery_at FROM subscription_tv_episode_discovery WHERE subscription_id=$1 AND unit_id=$2;
