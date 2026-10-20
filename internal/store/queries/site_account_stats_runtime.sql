-- name: ListDueSiteAccountStats :many
SELECT c.indexer_id FROM site_account_stats_config c
LEFT JOIN site_account_stats_schedule s ON s.indexer_id=c.indexer_id
WHERE c.enabled AND (s.indexer_id IS NULL OR s.config_version<>c.version OR (s.state='idle' AND s.next_due_at<=clock_timestamp()))
AND NOT EXISTS(SELECT 1 FROM site_account_stats_attempt a JOIN job j ON j.id=a.job_id WHERE a.indexer_id=c.indexer_id AND (a.state IN ('queued','running') OR j.state IN ('queued','running')))
ORDER BY COALESCE(s.next_due_at,'-infinity'::timestamptz),c.indexer_id LIMIT 64;

-- name: GetSiteAccountStatsSchedule :one
SELECT * FROM site_account_stats_schedule WHERE indexer_id=$1 FOR UPDATE;

-- name: QueueSiteAccountStatsSchedule :exec
INSERT INTO site_account_stats_schedule(indexer_id,config_version,active_job_id,state)
VALUES($1,$2,$3,'queued') ON CONFLICT(indexer_id) DO UPDATE SET config_version=EXCLUDED.config_version,active_job_id=EXCLUDED.active_job_id,state='queued',next_due_at=clock_timestamp(),updated_at=clock_timestamp();

-- name: CreateSiteAccountStatsJob :one
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES($1,'site_account_stats_collect',$2,'queued',$3,1,clock_timestamp(),clock_timestamp(),clock_timestamp()) RETURNING *;

-- name: CreateSiteAccountStatsAttempt :exec
INSERT INTO site_account_stats_attempt(job_id,indexer_id,config_version,source_version,state) VALUES($1,$2,$3,$4,'queued');

-- name: GetSiteAccountStatsAttempt :one
SELECT * FROM site_account_stats_attempt WHERE job_id=$1 FOR UPDATE;

-- name: TrySiteAccountStatsGate :one
SELECT pg_try_advisory_lock(hashtextextended('indexer:'||sqlc.arg(indexer_id)::uuid::text,0))::boolean AS held;

-- name: ReleaseSiteAccountStatsGate :one
SELECT pg_advisory_unlock(hashtextextended('indexer:'||sqlc.arg(indexer_id)::uuid::text,0))::boolean AS released;

-- name: CheckSiteAccountStatsGate :one
SELECT pulse_stats_gate_held(sqlc.arg(indexer_id)::uuid)::boolean AS held;

-- name: ArmSiteAccountStatsCommit :one
SELECT set_config('pulse.stats_binding',pulse_stats_binding(sqlc.arg(indexer_id)::uuid),true)::text AS binding;

-- name: SiteAccountStatsBinding :one
SELECT pulse_stats_binding(sqlc.arg(indexer_id)::uuid)::text AS binding;

-- name: BindSiteAccountStatsAttempt :execrows
UPDATE site_account_stats_attempt SET execution_owner=$2,execution_token=$3 WHERE job_id=$1 AND state='queued' AND execution_owner IS NULL;

-- name: StartSiteAccountStatsAttempt :execrows
UPDATE site_account_stats_attempt SET state='running',started_at=clock_timestamp() WHERE job_id=$1 AND state='queued' AND execution_owner=$2 AND execution_token=$3;

-- name: StartSiteAccountStatsSchedule :execrows
UPDATE site_account_stats_schedule SET state='running',updated_at=clock_timestamp() WHERE indexer_id=$1 AND active_job_id=$2 AND config_version=$3 AND state='queued';

-- name: FinishSiteAccountStatsAttempt :execrows
UPDATE site_account_stats_attempt SET state=$2,sample=$3,failure_code=$4,finished_at=clock_timestamp() WHERE job_id=$1 AND state='running';

-- name: FinishSiteAccountStatsSchedule :execrows
UPDATE site_account_stats_schedule SET state='idle',active_job_id=NULL,next_due_at=clock_timestamp()+sqlc.arg(interval_seconds)::integer*interval '1 second',updated_at=clock_timestamp()
WHERE indexer_id=$1 AND active_job_id=$2 AND config_version=$3;

-- name: FinishSiteAccountStatsJob :execrows
UPDATE job SET state=$4,result=$5,error=$6,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=$1 AND kind='site_account_stats_collect' AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp();

-- name: DeferSiteAccountStatsBudget :exec
UPDATE indexer SET rate_limit_until=GREATEST(COALESCE(rate_limit_until,'-infinity'::timestamptz),clock_timestamp()+sqlc.arg(retry_ms)::bigint*interval '1 millisecond'),
 next_query_at=GREATEST(next_query_at,clock_timestamp()+sqlc.arg(retry_ms)::bigint*interval '1 millisecond') WHERE id=$1;

-- name: ListExpiredSiteAccountStats :many
SELECT j.id FROM job j JOIN site_account_stats_attempt a ON a.job_id=j.id
WHERE j.kind='site_account_stats_collect' AND a.state IN ('queued','running')
AND (j.state IN ('dead','succeeded') OR (j.state='running' AND j.lease_until<=clock_timestamp()) OR (j.state='queued' AND j.attempts>0))
ORDER BY j.id LIMIT 64 FOR UPDATE OF j SKIP LOCKED;

-- name: ReviewSiteAccountStatsAttempt :exec
UPDATE site_account_stats_attempt SET state='review_required',failure_code='unavailable',finished_at=clock_timestamp() WHERE job_id=$1 AND state IN ('queued','running');

-- name: ReviewSiteAccountStatsSchedule :exec
UPDATE site_account_stats_schedule SET state='review_required',active_job_id=NULL,updated_at=clock_timestamp() WHERE active_job_id=$1;

-- name: ReviewSiteAccountStatsJob :exec
UPDATE job SET state='dead',error='unavailable',lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=$1 AND state IN ('queued','running');

-- name: AcquireSiteAccountStatsGate :exec
SELECT pg_advisory_lock(hashtextextended('indexer:'||sqlc.arg(indexer_id)::uuid::text,0));
