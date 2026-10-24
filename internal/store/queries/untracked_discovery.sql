-- name: UntrackedDiscoveryScopeOwner :one
SELECT owner_id FROM untracked_directory_scope WHERE id=$1;

-- name: UntrackedDiscoveryReadAdmin :one
SELECT EXISTS(SELECT 1 FROM app_user WHERE id=$1 AND is_active AND role='admin')::boolean AS allowed;

-- name: UntrackedDiscoveryClock :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: ListUntrackedDiscoveryCandidates :many
SELECT id FROM untracked_directory_scope
WHERE enabled AND id>sqlc.arg(after_id)::uuid AND created_at<=sqlc.arg(horizon)::timestamptz
ORDER BY id LIMIT sqlc.arg(page_size)::integer;

-- name: ListUntrackedDiscoveryDue :many
SELECT s.id FROM untracked_directory_scope s LEFT JOIN untracked_discovery_state d ON d.scope_id=s.id
LEFT JOIN job j ON j.id=d.active_job_id
WHERE s.enabled AND s.id>sqlc.arg(after_id)::uuid AND (d.scope_id IS NULL OR
 ((j.id IS NULL OR j.state NOT IN ('queued','running')) AND
 (d.requested_version<>s.version OR d.requested_sequence>d.completed_sequence OR d.next_sweep_at<=clock_timestamp()))
 OR (j.kind='untracked_discovery' AND j.state='running' AND j.lease_until<=clock_timestamp()))
ORDER BY s.id LIMIT sqlc.arg(page_size)::integer;

-- name: EnsureUntrackedDiscoveryState :exec
INSERT INTO untracked_discovery_state(scope_id,requested_version)
VALUES(sqlc.arg(scope_id)::uuid,sqlc.arg(version)::bigint) ON CONFLICT(scope_id) DO NOTHING;

-- name: LockUntrackedDiscoveryState :one
SELECT to_jsonb(d)::jsonb AS document FROM untracked_discovery_state d WHERE scope_id=$1 FOR UPDATE;

-- name: GetUntrackedDiscoveryState :one
SELECT to_jsonb(d)::jsonb AS document FROM untracked_discovery_state d WHERE scope_id=$1;

-- name: RequestUntrackedDiscoverySequence :exec
UPDATE untracked_discovery_state SET requested_version=sqlc.arg(version)::bigint,requested_sequence=requested_sequence+1 WHERE scope_id=sqlc.arg(scope_id)::uuid;

-- name: CreateUntrackedDiscoveryJob :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,'untracked_discovery',(sqlc.arg(id)::uuid)::text,'queued',sqlc.arg(payload)::jsonb,3,clock_timestamp(),clock_timestamp(),clock_timestamp());

-- name: SetUntrackedDiscoveryActive :exec
UPDATE untracked_discovery_state SET active_job_id=sqlc.arg(job_id)::uuid,active_sequence=requested_sequence,scheduled_scope_version=requested_version WHERE scope_id=sqlc.arg(scope_id)::uuid;

-- name: FinishUntrackedDiscoveryState :exec
UPDATE untracked_discovery_state SET completed_sequence=active_sequence,active_job_id=NULL,next_sweep_at=clock_timestamp()+interval '60 seconds' WHERE scope_id=$1;

-- name: RecoverUntrackedDiscoveryJob :execrows
UPDATE job SET state=CASE WHEN attempts>=3 THEN 'dead' ELSE 'queued' END,
lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp(),
finished_at=CASE WHEN attempts>=3 THEN clock_timestamp() ELSE NULL END
WHERE id=$1 AND kind='untracked_discovery' AND max_attempts=3 AND state='running' AND lease_until<=clock_timestamp();

-- name: FinishUntrackedDiscoveryJob :execrows
UPDATE job SET state='succeeded',result='{}',error=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=sqlc.arg(id)::uuid AND kind='untracked_discovery' AND state='running'
AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp();

-- Retain original lease columns on completion, including through deferred effects.
-- name: CheckUntrackedDiscoveryFinishedLease :one
SELECT EXISTS(SELECT 1 FROM job WHERE id=sqlc.arg(id)::uuid AND kind='untracked_discovery' AND state='succeeded' AND max_attempts=3
AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint
AND lease_until>clock_timestamp() AND payload=sqlc.arg(payload)::jsonb)::boolean AS valid;

-- name: RecordUntrackedDiscoveryFile :exec
INSERT INTO untracked_discovered_file AS f(id,scope_id,scope_version,relative_path,device,inode,size,mtime,last_job_id,last_token)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(scope_id)::uuid,sqlc.arg(scope_version)::bigint,sqlc.arg(relative_path)::text,
sqlc.arg(device)::bigint,sqlc.arg(inode)::bigint,sqlc.arg(size)::bigint,sqlc.arg(mtime)::bigint,sqlc.arg(job_id)::uuid,sqlc.arg(token)::bigint)
ON CONFLICT(scope_id,relative_path) DO UPDATE SET
scope_version=EXCLUDED.scope_version,device=EXCLUDED.device,inode=EXCLUDED.inode,size=EXCLUDED.size,mtime=EXCLUDED.mtime,
revision=CASE WHEN (f.scope_version,f.device,f.inode,f.size,f.mtime)=(EXCLUDED.scope_version,EXCLUDED.device,EXCLUDED.inode,EXCLUDED.size,EXCLUDED.mtime) THEN f.revision ELSE f.revision+1 END,
first_observed_at=CASE WHEN (f.scope_version,f.device,f.inode,f.size,f.mtime)=(EXCLUDED.scope_version,EXCLUDED.device,EXCLUDED.inode,EXCLUDED.size,EXCLUDED.mtime) THEN f.first_observed_at ELSE EXCLUDED.first_observed_at END,
observations=CASE WHEN (f.scope_version,f.device,f.inode,f.size,f.mtime)=(EXCLUDED.scope_version,EXCLUDED.device,EXCLUDED.inode,EXCLUDED.size,EXCLUDED.mtime) THEN f.observations+1 ELSE 1 END,
last_observed_at=EXCLUDED.last_observed_at,last_job_id=EXCLUDED.last_job_id,last_token=EXCLUDED.last_token
WHERE (f.last_job_id,f.last_token) IS DISTINCT FROM (EXCLUDED.last_job_id,EXCLUDED.last_token);

-- name: UntrackedDiscoveryBatchSeal :one
SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY f.id),'[]'::jsonb)::jsonb AS document FROM untracked_discovered_file f
WHERE scope_id=sqlc.arg(scope_id)::uuid AND last_job_id=sqlc.arg(job_id)::uuid AND last_token=sqlc.arg(token)::bigint
AND relative_path=ANY(sqlc.arg(paths)::text[]);

-- Creation order stays fixed when observations change; last_observed_at is telemetry.
-- name: ReadUntrackedDiscoveryFiles :many
SELECT to_jsonb(f)::jsonb AS document FROM untracked_discovered_file f
WHERE scope_id=sqlc.arg(scope_id)::uuid AND created_at<=sqlc.arg(horizon)::timestamptz
AND (created_at,id)>(sqlc.arg(after_created)::timestamptz,sqlc.arg(after_id)::uuid)
ORDER BY created_at,id LIMIT sqlc.arg(page_size)::integer;
