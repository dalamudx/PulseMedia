-- name: ListDueDownloadGlobalRates :many
SELECT c.id FROM download_client c LEFT JOIN download_global_rate_schedule s ON s.client_id=c.id
WHERE c.client_type IN ('qbittorrent','transmission','rtorrent') AND (s.client_id IS NULL OR (s.active_job_id IS NULL AND s.next_due_at<=clock_timestamp()))
ORDER BY COALESCE(s.next_due_at,'-infinity'::timestamptz),c.id LIMIT 64;

-- name: DownloadGlobalRatesBinding :one
SELECT COALESCE(pulse_global_rates_binding(sqlc.arg(client_id)::uuid),'')::text AS binding;

-- name: CreateDownloadGlobalRatesJob :one
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES($1,'download_global_rates',$2,'queued',$3,1,clock_timestamp(),clock_timestamp(),clock_timestamp()) RETURNING *;

-- name: CreateDownloadGlobalRatesAttempt :exec
INSERT INTO download_global_rate_attempt(job_id,client_id,config_stamp,state) VALUES($1,$2,$3,'queued');

-- name: QueueDownloadGlobalRatesSchedule :exec
INSERT INTO download_global_rate_schedule(client_id,active_job_id) VALUES($1,$2)
ON CONFLICT(client_id) DO UPDATE SET active_job_id=EXCLUDED.active_job_id,updated_at=clock_timestamp();

-- name: GetDownloadGlobalRatesSchedule :one
SELECT * FROM download_global_rate_schedule WHERE client_id=$1 FOR UPDATE;

-- name: GetDownloadGlobalRatesAttempt :one
SELECT * FROM download_global_rate_attempt WHERE job_id=$1 FOR UPDATE;

-- name: AcquireDownloadGlobalRatesGate :exec
SELECT pg_advisory_lock(hashtextextended('download-global-rates:'||sqlc.arg(client_id)::uuid::text,0));

-- name: TryDownloadGlobalRatesRecoveryGate :one
SELECT pg_try_advisory_xact_lock(hashtextextended('download-global-rates:'||sqlc.arg(client_id)::uuid::text,0))::boolean AS held;

-- name: CheckDownloadGlobalRatesGate :one
SELECT pulse_global_rates_gate_held(sqlc.arg(client_id)::uuid)::boolean AS held;

-- name: ReleaseDownloadGlobalRatesGate :one
SELECT pg_advisory_unlock(hashtextextended('download-global-rates:'||sqlc.arg(client_id)::uuid::text,0))::boolean AS released;

-- name: StartDownloadGlobalRatesAttempt :execrows
UPDATE download_global_rate_attempt SET state='running',execution_owner=$2,execution_token=$3,started_at=clock_timestamp() WHERE job_id=$1 AND state='queued' AND execution_owner IS NULL;

-- name: FinishDownloadGlobalRatesAttempt :execrows
UPDATE download_global_rate_attempt SET state=sqlc.arg(state),observed_at=sqlc.arg(observed_at)::timestamptz,
 download_bytes_per_second=sqlc.narg(download_bytes_per_second)::bigint,upload_bytes_per_second=sqlc.narg(upload_bytes_per_second)::bigint,
 failure_code=sqlc.arg(failure_code),finished_at=clock_timestamp()
WHERE job_id=sqlc.arg(job_id) AND state='running';

-- name: FinishDownloadGlobalRatesSchedule :execrows
UPDATE download_global_rate_schedule SET active_job_id=NULL,latest_job_id=sqlc.arg(job_id),
 last_good_job_id=CASE WHEN sqlc.arg(success)::boolean THEN sqlc.arg(job_id) ELSE last_good_job_id END,
 next_due_at=clock_timestamp()+sqlc.arg(delay_ms)::bigint*interval '1 millisecond',updated_at=clock_timestamp()
WHERE client_id=sqlc.arg(client_id) AND active_job_id=sqlc.arg(job_id);

-- name: FinishDownloadGlobalRatesJob :execrows
UPDATE job SET state='succeeded',result='{}'::jsonb,error=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=$1 AND kind='download_global_rates' AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp();

-- name: ListExpiredDownloadGlobalRates :many
SELECT a.job_id,a.client_id FROM download_global_rate_attempt a JOIN job j ON j.id=a.job_id
WHERE a.state IN ('queued','running') AND (j.state IN ('dead','succeeded') OR (j.state='running' AND j.lease_until<=clock_timestamp()) OR (j.state='queued' AND j.attempts>0))
ORDER BY a.job_id LIMIT 64 FOR UPDATE OF j SKIP LOCKED;

-- name: AbandonDownloadGlobalRatesAttempt :execrows
UPDATE download_global_rate_attempt SET state='abandoned',failure_code='unavailable',finished_at=clock_timestamp() WHERE job_id=$1 AND state IN ('queued','running');

-- name: AbandonDownloadGlobalRatesSchedule :execrows
UPDATE download_global_rate_schedule SET active_job_id=NULL,latest_job_id=$2,next_due_at=clock_timestamp()+interval '10 seconds',updated_at=clock_timestamp() WHERE client_id=$1 AND active_job_id=$2;

-- name: AbandonDownloadGlobalRatesJob :execrows
UPDATE job SET state='dead',error='global rate observation interrupted',lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state IN ('queued','running');
