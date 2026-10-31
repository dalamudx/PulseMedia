-- name: ListDueCookieCloudOwners :many
SELECT c.owner_id,c.version,COALESCE(s.next_due_at,c.updated_at)::timestamptz AS scan_at FROM cookiecloud_configuration c JOIN app_user u ON u.id=c.owner_id
LEFT JOIN cookiecloud_sync_schedule s ON s.owner_id=c.owner_id
WHERE c.enabled AND u.is_active AND u.role='admin'
AND (sqlc.arg(after_owner_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid
 OR (COALESCE(s.next_due_at,c.updated_at),c.owner_id)>(sqlc.arg(after_scan_at)::timestamptz,sqlc.arg(after_owner_id)::uuid))
AND (s.owner_id IS NULL OR s.state='idle')
AND (CASE WHEN s.config_version=c.version THEN s.next_due_at ELSE c.updated_at+c.interval_seconds*interval '1 second' END)<=clock_timestamp()
AND NOT EXISTS(SELECT 1 FROM cookiecloud_sync_attempt a WHERE a.owner_id=c.owner_id AND a.state IN ('queued','running','review_required'))
ORDER BY COALESCE(s.next_due_at,c.updated_at),c.owner_id LIMIT 64;

-- name: GetCookieCloudSyncSchedule :one
SELECT * FROM cookiecloud_sync_schedule WHERE owner_id=$1 FOR UPDATE;

-- name: QueueCookieCloudSyncSchedule :exec
INSERT INTO cookiecloud_sync_schedule(owner_id,config_version,next_due_at,active_job_id,state)
VALUES($1,$2,clock_timestamp(),$3,'queued') ON CONFLICT(owner_id) DO UPDATE SET config_version=EXCLUDED.config_version,active_job_id=EXCLUDED.active_job_id,state='queued',next_due_at=clock_timestamp(),updated_at=clock_timestamp();

-- name: CreateCookieCloudSyncJob :one
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES($1,'cookiecloud_sync',$2,'queued',$3,1,clock_timestamp(),clock_timestamp(),clock_timestamp()) RETURNING *;

-- name: CreateCookieCloudSyncAttempt :exec
INSERT INTO cookiecloud_sync_attempt(job_id,owner_id,config_version,state) VALUES($1,$2,$3,'queued');

-- name: GetCookieCloudSyncAttempt :one
SELECT * FROM cookiecloud_sync_attempt WHERE job_id=$1 FOR UPDATE;

-- name: CookieCloudSyncBinding :one
SELECT pulse_cookiecloud_sync_binding(sqlc.arg(owner_id)::uuid)::text AS binding;

-- name: ArmCookieCloudSyncCommit :one
SELECT set_config('pulse.cookiecloud_binding',pulse_cookiecloud_sync_binding(sqlc.arg(owner_id)::uuid),true)::text AS binding;

-- name: TryCookieCloudSyncGate :one
SELECT pg_try_advisory_lock(hashtextextended('cookiecloud:'||sqlc.arg(owner_id)::uuid::text,0))::boolean AS held;

-- name: AcquireCookieCloudSyncGate :exec
SELECT pg_advisory_lock(hashtextextended('cookiecloud:'||sqlc.arg(owner_id)::uuid::text,0));

-- name: ReleaseCookieCloudSyncGate :one
SELECT pg_advisory_unlock(hashtextextended('cookiecloud:'||sqlc.arg(owner_id)::uuid::text,0))::boolean AS released;

-- name: CheckCookieCloudSyncGate :one
SELECT pulse_cookiecloud_sync_gate_held(sqlc.arg(owner_id)::uuid)::boolean AS held;

-- name: BindCookieCloudSyncAttempt :execrows
UPDATE cookiecloud_sync_attempt SET execution_owner=$2,execution_token=$3,execution_pid=pg_backend_pid()
WHERE job_id=$1 AND state='queued' AND execution_owner IS NULL;

-- name: StartCookieCloudSyncAttempt :execrows
UPDATE cookiecloud_sync_attempt SET state='running',started_at=clock_timestamp() WHERE job_id=$1 AND state='queued' AND execution_owner=$2 AND execution_token=$3 AND execution_pid=pg_backend_pid();

-- name: StartCookieCloudSyncSchedule :execrows
UPDATE cookiecloud_sync_schedule SET state='running',updated_at=clock_timestamp() WHERE owner_id=$1 AND active_job_id=$2 AND config_version=$3 AND state='queued';

-- name: FinishCookieCloudSyncAttempt :execrows
UPDATE cookiecloud_sync_attempt SET state=$2,result=$3,failure_code=$4,valid_until=$5,finished_at=clock_timestamp() WHERE job_id=$1 AND state='running';

-- name: FinishCookieCloudSyncSchedule :execrows
UPDATE cookiecloud_sync_schedule SET state='idle',active_job_id=NULL,next_due_at=clock_timestamp()+sqlc.arg(interval_seconds)::integer*interval '1 second',updated_at=clock_timestamp()
WHERE owner_id=$1 AND active_job_id=$2 AND config_version=$3;

-- name: FinishCookieCloudSyncJob :execrows
UPDATE job SET state=$4,result=$5,error=$6,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=$1 AND kind='cookiecloud_sync' AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp();

-- name: ListExpiredCookieCloudSync :many
SELECT a.* FROM cookiecloud_sync_attempt a JOIN job j ON j.id=a.job_id
WHERE a.state IN ('queued','running') AND (j.state IN ('dead','succeeded') OR (j.state='running' AND j.lease_until<=clock_timestamp()) OR (j.state='queued' AND j.attempts>0))
ORDER BY a.job_id LIMIT 64 FOR UPDATE OF j SKIP LOCKED;

-- name: ReviewCookieCloudSyncAttempt :exec
UPDATE cookiecloud_sync_attempt SET state='review_required',failure_code='unavailable',finished_at=clock_timestamp() WHERE job_id=$1 AND state IN ('queued','running');

-- name: ReviewCookieCloudSyncSchedule :exec
UPDATE cookiecloud_sync_schedule SET state='review_required',active_job_id=NULL,updated_at=clock_timestamp() WHERE active_job_id=$1;

-- name: ReviewCookieCloudSyncJob :exec
UPDATE job SET state='dead',error='unavailable',lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state IN ('queued','running');

-- name: ListCookieCloudSources :many
SELECT i.* FROM indexer i JOIN indexer_configuration ic ON ic.indexer_id=i.id LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id
WHERE ic.protocol='nexusphp' AND COALESCE(l.enabled,true) AND l.deleted_at IS NULL ORDER BY i.id;

-- name: LockCookieCloudSyncSourceGate :exec
SELECT pg_advisory_xact_lock(hashtextextended('indexer:'||sqlc.arg(indexer_id)::uuid::text,0));

-- name: LockCookieCloudSyncSourceConfiguration :one
SELECT * FROM indexer_configuration WHERE indexer_id=$1 FOR UPDATE;

-- name: UpdateCookieCloudSourceCredential :one
UPDATE indexer SET api_key_encrypted=sqlc.arg(credential_ciphertext),config_version=config_version+1,updated_at=clock_timestamp()
WHERE id=sqlc.arg(indexer_id) AND config_version=sqlc.arg(expected_version) RETURNING *;

-- name: AdvanceCookieCloudSourceConfiguration :execrows
UPDATE indexer_configuration SET configuration_version=sqlc.arg(new_version)
WHERE indexer_id=sqlc.arg(indexer_id) AND configuration_version=sqlc.arg(expected_version);

-- name: CookieCloudCredentialSchemaStatus :one
SELECT (to_regclass('indexer_cookiecloud_credential') IS NOT NULL)::boolean AS available,
 (to_regprocedure('pulse_cookiecloud_credential_commit_guard()') IS NOT NULL)::boolean AS required;

-- name: RecordCookieCloudCredentialEvidence :execrows
INSERT INTO indexer_cookiecloud_credential(job_id,indexer_id,source_version,credential_digest,valid_until)
SELECT sqlc.arg(job_id),i.id,i.config_version,sha256(i.api_key_encrypted),sqlc.narg(valid_until)::timestamptz
FROM indexer i WHERE i.id=sqlc.arg(indexer_id) AND i.config_version=sqlc.arg(source_version);

-- name: GetCookieCloudCredentialEvidence :one
SELECT * FROM indexer_cookiecloud_credential WHERE indexer_id=sqlc.arg(indexer_id) AND source_version=sqlc.arg(source_version)
ORDER BY recorded_at DESC,job_id DESC LIMIT 1;
