-- name: ListDueHealthIndexers :many
SELECT i.* FROM indexer i WHERE health_next_check_at<=clock_timestamp()
 AND next_query_at<=clock_timestamp() AND (rate_limit_until IS NULL OR rate_limit_until<=clock_timestamp())
 AND (rss_enabled OR EXISTS(SELECT 1 FROM subscription s WHERE s.state='active' AND s.indexer_ids @> jsonb_build_array(i.id::text)))
 ORDER BY health_next_check_at,id LIMIT $1 FOR UPDATE OF i SKIP LOCKED;
-- name: QueueIndexerHealth :execrows
INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before,created_at,updated_at)
SELECT $1,'indexer_health',i.id::text||':'||i.config_version::text,6,'queued',jsonb_build_object('indexer_id',i.id,'config_version',i.config_version),8,clock_timestamp(),clock_timestamp(),clock_timestamp() FROM indexer i WHERE i.id=$2
ON CONFLICT(kind,dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued','running') DO NOTHING;
-- name: AdvanceIndexerHealthSchedule :exec
UPDATE indexer SET health_next_check_at=clock_timestamp()+interval '5 minutes' WHERE id=$1;
-- name: RecordIndexerHealth :one
UPDATE indexer SET health=CASE WHEN sqlc.arg(reason)::text='rate_limited' THEN health WHEN sqlc.arg(reason)::text='ok' THEN 'online' ELSE 'offline' END,
 health_checked_at=clock_timestamp(),health_observed_at=sqlc.arg(observed_at),health_query_id=sqlc.arg(query_id),health_reason=sqlc.arg(reason),
 health_failures=CASE WHEN sqlc.arg(reason)::text='ok' THEN 0 ELSE LEAST(health_failures+1,16) END,
 health_next_check_at=GREATEST(next_query_at,COALESCE(rate_limit_until,'-infinity'::timestamptz),clock_timestamp()+
 (CASE WHEN sqlc.arg(reason)::text='ok' THEN 900 ELSE LEAST(3600,30*power(2,LEAST(health_failures,7))) END*(1+(get_byte(uuid_send(id),0)%21)/100.0))*interval '1 second'),updated_at=clock_timestamp()
WHERE id=$1 AND config_version=$2 AND (health_observed_at IS NULL OR health_observed_at<sqlc.arg(observed_at)) RETURNING *;
