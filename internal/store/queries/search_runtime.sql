-- name: GetAuthorizedSearchJob :one
SELECT * FROM job WHERE id=$1 AND kind=$2 AND state='running' AND lease_owner=$3 AND lease_token=$4 AND lease_until>clock_timestamp();
-- name: LockSearchPolicy :one
SELECT * FROM policy WHERE id=$1 FOR UPDATE;
-- name: LockSearchSubscription :one
SELECT * FROM subscription WHERE id=$1 FOR UPDATE;
-- name: LockSearchRound :one
SELECT * FROM search_round WHERE id=$1 FOR UPDATE;
-- name: FreezeSearchRound :one
UPDATE search_round SET decision_snapshot=$2 WHERE id=$1 AND state='running' RETURNING *;
-- name: SetRoundDecision :one
UPDATE search_round SET state=$2,selected_candidate_id=$3,operation_id=$4,abort_reason=$5,finished_at=clock_timestamp()
WHERE id=$1 AND state='running' RETURNING *;
-- name: AddRoundEndpoint :exec
INSERT INTO search_round_endpoint(round_id,indexer_id) VALUES($1,$2);
-- name: ReturnRoundEndpoint :execrows
UPDATE search_round_endpoint SET query_id=$3,state=$4 WHERE round_id=$1 AND indexer_id=$2 AND state='pending';
-- name: LinkQueryCandidate :exec
INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2) ON CONFLICT DO NOTHING;
-- name: ListQueryCandidates :many
SELECT c.* FROM release_candidate c JOIN indexer_query_candidate qc ON qc.candidate_id=c.id WHERE qc.query_id=$1 AND EXISTS(SELECT 1 FROM indexer_query q WHERE q.id=$1 AND q.config_version=c.source_config_version) ORDER BY c.id;
-- name: ListRoundQueryCandidates :many
SELECT DISTINCT c.* FROM release_candidate c JOIN indexer_query_candidate qc ON qc.candidate_id=c.id
JOIN search_round_endpoint e ON e.query_id=qc.query_id JOIN indexer_query q ON q.id=e.query_id
JOIN search_round r ON r.id=e.round_id
WHERE e.round_id=$1 AND q.state='succeeded' AND q.finished_at<=r.deadline AND c.source_config_version=q.config_version AND q.config_version=(r.decision_snapshot->'indexers'->>e.indexer_id::text)::bigint
UNION
SELECT c.* FROM release_candidate c JOIN indexer_query_candidate qc ON qc.candidate_id=c.id
JOIN search_round_endpoint_page p ON p.query_id=qc.query_id JOIN indexer_query q ON q.id=p.query_id JOIN search_round r ON r.id=p.round_id
WHERE p.round_id=$1 AND q.state='succeeded' AND q.finished_at<=r.deadline
 AND c.source_config_version=q.config_version AND q.config_version=(r.decision_snapshot->'indexers'->>p.indexer_id::text)::bigint
UNION
SELECT c.* FROM release_candidate c JOIN indexer_query_candidate qc ON qc.candidate_id=c.id
JOIN indexer_query q ON q.id=qc.query_id JOIN search_round_rss_query rq ON rq.query_id=q.id JOIN search_round r ON r.id=rq.round_id
WHERE rq.round_id=$1 AND q.state='succeeded' AND q.finished_at<=r.deadline
 AND c.source_config_version=q.config_version AND q.config_version=(r.decision_snapshot->'indexers'->>q.indexer_id::text)::bigint ORDER BY id;
-- name: ListReadySearchRounds :many
SELECT r.* FROM search_round r WHERE r.state='running' AND r.decision_snapshot->>'scheduled'='true'
AND (r.deadline<=clock_timestamp() OR NOT EXISTS (
 SELECT 1 FROM search_round_endpoint e LEFT JOIN indexer_query q ON q.id=e.query_id
 WHERE e.round_id=r.id AND (e.state='pending' OR q.state='running')))
ORDER BY r.deadline,r.id LIMIT $1;
-- name: GetSearchDownloaderSnapshot :one
SELECT id,encode(sha256(convert_to(jsonb_build_object('type',client_type,'url',url,'user',username,'cipher',encode(password_encrypted,'hex'),'map',remote_path_map)::text,'UTF8')),'hex') AS config_digest FROM download_client WHERE id=$1 FOR SHARE;
-- name: GetSearchDatabaseTime :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: LockSearchMedia :one
SELECT * FROM media WHERE id=$1 FOR SHARE;
-- name: GetSearchUnit :one
SELECT * FROM media_unit WHERE id=$1;
-- name: QueueRoundFinalization :exec
INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before,created_at,updated_at)
SELECT $1,'search_round_finalize',r.id::text,6,'queued',jsonb_build_object('round_id',r.id),8,clock_timestamp(),clock_timestamp(),clock_timestamp()
FROM search_round r WHERE r.id=$2 AND r.state='running'
ON CONFLICT(kind,dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued','running') DO NOTHING;

-- name: LockSearchWanted :one
SELECT * FROM wanted_item WHERE id=$1 FOR UPDATE;
-- name: LockSearchIndexer :one
SELECT * FROM indexer WHERE id=$1 FOR SHARE;
-- name: ListSearchMediaIdentities :many
SELECT * FROM media_identity WHERE media_id=$1;
-- name: IsSearchRoundReady :one
SELECT deadline<=clock_timestamp() OR NOT EXISTS (
 SELECT 1 FROM search_round_endpoint e LEFT JOIN indexer_query q ON q.id=e.query_id
 WHERE e.round_id=r.id AND (e.state='pending' OR q.state='running')) AS ready
FROM search_round r WHERE r.id=$1;

-- name: GetQueryExecution :one
SELECT * FROM indexer_query WHERE id=$1;
-- name: RetireInterruptedIndexerQueries :exec
UPDATE indexer_query SET state='failed',error='execution interrupted',finished_at=clock_timestamp() WHERE indexer_id=$1 AND state='running';
-- name: ReserveIndexerBudget :one
UPDATE indexer SET next_query_at=clock_timestamp()+sqlc.arg(interval_ms)::bigint*interval '1 millisecond'
WHERE id=$1 AND next_query_at<=clock_timestamp()
AND (rate_limit_until IS NULL OR rate_limit_until<=clock_timestamp()) RETURNING *;
-- name: DeferIndexerBudget :exec
UPDATE indexer SET next_query_at=GREATEST(next_query_at,clock_timestamp()+sqlc.arg(interval_ms)::bigint*interval '1 millisecond') WHERE id=$1;
-- name: RecoverSearchJobs :execrows
UPDATE job SET state=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'queued' END,
lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE kind IN ('search_endpoint','search_round_finalize','rss_poll','indexer_health') AND state='running' AND lease_until<=clock_timestamp();
-- name: RescheduleWantedEvaluation :exec
UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+sqlc.arg(delay_ms)::bigint*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=$1;
