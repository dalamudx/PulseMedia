-- name: ConsumeWantedRSSObservation :exec
UPDATE wanted_item SET rss_seen_at=GREATEST(rss_seen_at,$2::timestamptz) WHERE id=$1;
-- name: LockRSSIndexer :one
SELECT * FROM indexer WHERE id=$1 FOR UPDATE;
-- name: ConfigureIndexerRSS :one
UPDATE indexer SET rss_enabled=$2,rss_kind=$3,rss_url_encrypted=$4,rss_interval_seconds=$5,
 config_version=config_version+1,rss_next_poll_at=clock_timestamp(),rss_last_query_id=NULL,rss_last_success_at=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND config_version=$6 RETURNING *;
-- name: CancelOldRSSJobs :exec
UPDATE job SET state='dead',lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,
 error='rss configuration changed',updated_at=clock_timestamp(),finished_at=clock_timestamp()
WHERE kind='rss_poll' AND payload->>'indexer_id'=$1::text AND state IN ('queued','running');
-- name: ListDueRSSIndexers :many
SELECT * FROM indexer WHERE rss_enabled AND health='online' AND rss_next_poll_at<=clock_timestamp()
 AND next_query_at<=clock_timestamp() AND (rate_limit_until IS NULL OR rate_limit_until<=clock_timestamp())
ORDER BY rss_next_poll_at,id LIMIT $1 FOR UPDATE SKIP LOCKED;
-- name: QueueRSSPoll :execrows
INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before,created_at,updated_at)
SELECT $1,'rss_poll',i.id::text||':'||i.config_version::text,7,'queued',
 jsonb_build_object('indexer_id',i.id,'config_version',i.config_version),8,clock_timestamp(),clock_timestamp(),clock_timestamp()
FROM indexer i WHERE i.id=$2 AND i.rss_enabled
ON CONFLICT(kind,dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued','running') DO NOTHING;
-- name: AdvanceRSSSchedule :exec
UPDATE indexer SET rss_next_poll_at=clock_timestamp()+
 (rss_interval_seconds+rss_interval_seconds*((get_byte(uuid_send(id),0)%21)-10)/100)*interval '1 second'
WHERE id=$1;
-- name: GetRSSJobQuery :one
SELECT * FROM indexer_query WHERE rss_job_id=$1 AND config_version=$2 AND state='succeeded' ORDER BY finished_at DESC LIMIT 1;
-- name: BindSharedRSSQuery :exec
UPDATE indexer_query SET rss_job_id=$2 WHERE id=$1 AND rss_job_id IS NULL;
-- name: GetLatestRSSQuery :one
SELECT q.* FROM indexer i JOIN indexer_query q ON q.id=i.rss_last_query_id
WHERE i.id=$1 AND i.config_version=$2 AND i.rss_enabled AND q.config_version=i.config_version
 AND q.state='succeeded' AND q.finished_at>clock_timestamp()-interval '2 minutes';
-- name: CountRSSQueryCandidates :one
SELECT count(*) FROM indexer_query_candidate WHERE query_id=$1;
-- name: SignalRSSWanteds :exec
WITH eligible AS (
 SELECT w.id FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
 WHERE s.state='active' AND NOT w.is_skipped AND s.indexer_ids @> jsonb_build_array($1::uuid::text)
 ORDER BY w.id FOR UPDATE OF w SKIP LOCKED
)
UPDATE wanted_item w SET next_evaluate_at=clock_timestamp(),updated_at=clock_timestamp() FROM eligible e WHERE w.id=e.id;
-- name: AttachRSSToRunningRounds :exec
WITH eligible AS (
 SELECT r.id,q.id AS query_id FROM search_round r,indexer_query q
 WHERE q.id=$1 AND r.state='running' AND r.deadline>clock_timestamp()
  AND q.state='succeeded' AND q.finished_at<=r.deadline
  AND q.config_version=(r.decision_snapshot->'indexers'->>q.indexer_id::text)::bigint
 FOR KEY SHARE OF r SKIP LOCKED
)
INSERT INTO search_round_rss_query(round_id,query_id) SELECT id,query_id FROM eligible ON CONFLICT DO NOTHING;
-- name: LinkRoundRSSQuery :exec
INSERT INTO search_round_rss_query(round_id,query_id) VALUES($1,$2) ON CONFLICT DO NOTHING;
-- name: AcknowledgeRSSQuery :one
UPDATE indexer SET rss_last_query_id=$2,rss_last_success_at=clock_timestamp()
WHERE id=$1 AND config_version=$3 AND rss_enabled RETURNING *;
