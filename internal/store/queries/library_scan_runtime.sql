-- name: InitializeLibraryScanSchedules :exec
INSERT INTO library_scan_schedule(root_id) SELECT id FROM library_root ON CONFLICT DO NOTHING;

-- name: LockDueLibraryScanRoots :many
SELECT r.id FROM library_root r JOIN library_scan_schedule s ON s.root_id=r.id
LEFT JOIN job previous ON previous.id=s.last_scan_job_id
WHERE s.next_run_at<=clock_timestamp()
 AND (previous.id IS NULL OR previous.state='succeeded')
 AND NOT EXISTS(SELECT 1 FROM library_scan_job l JOIN job j ON j.id=l.job_id
 WHERE l.root_id=r.id AND j.state IN('queued','running','dead'))
ORDER BY s.next_run_at,r.id LIMIT $1 FOR UPDATE OF r,s SKIP LOCKED;


-- name: IsLibraryScanRootDue :one
-- Run in a fresh READ COMMITTED statement after LockDueLibraryScanRoots.
-- Lock acquisition can follow another scheduler's commit while the previous
-- statement still sees an older job snapshot.
SELECT EXISTS (
 SELECT 1 FROM library_root r JOIN library_scan_schedule s ON s.root_id=r.id
 LEFT JOIN job previous ON previous.id=s.last_scan_job_id
 WHERE r.id=$1 AND s.next_run_at<=clock_timestamp()
 AND (previous.id IS NULL OR previous.state='succeeded')
 AND NOT EXISTS (SELECT 1 FROM library_scan_job l JOIN job j ON j.id=l.job_id
 WHERE l.root_id=r.id AND j.state IN('queued','running','dead'))
);
-- name: InsertLibraryScanJob :exec
WITH inserted AS (
 INSERT INTO job(id,kind,state,payload,priority,max_attempts)
 VALUES(sqlc.arg(job_id)::uuid,'library_scan','queued',jsonb_build_object('root_id',sqlc.arg(root_id)::uuid),10,5)
 RETURNING id
)
INSERT INTO library_scan_job(job_id,root_id,phase) SELECT id,sqlc.arg(root_id)::uuid,'scan' FROM inserted;

-- name: SetLibraryScheduledJob :exec
UPDATE library_scan_schedule SET last_scan_job_id=sqlc.arg(job_id) WHERE root_id=sqlc.arg(root_id);

-- name: GetLibraryScanWork :one
SELECT l.* FROM library_scan_job l JOIN job j ON j.id=l.job_id WHERE l.job_id=$1
 AND j.kind=CASE l.phase WHEN 'scan' THEN 'library_scan' ELSE 'library_missing' END;

-- name: GuardOwnedLibraryScanWork :one
WITH locked AS MATERIALIZED (
 SELECT j.id,j.kind,j.state,j.lease_owner,j.lease_token,j.lease_until,l.root_id,l.phase,l.run_id
 FROM job j JOIN library_scan_job l ON l.job_id=j.id WHERE j.id=sqlc.arg(job_id)::uuid FOR UPDATE OF j
)
SELECT root_id,phase,run_id FROM locked
WHERE root_id=sqlc.arg(root_id)::uuid AND phase=sqlc.arg(phase)::text
 AND kind=CASE phase WHEN 'scan' THEN 'library_scan' ELSE 'library_missing' END
 AND state='running' AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint
 AND lease_until>clock_timestamp();

-- name: BindLibraryScanRun :execrows
UPDATE library_scan_job l SET run_id=sqlc.arg(run_id)::uuid FROM scan_run fresh
WHERE l.job_id=sqlc.arg(job_id)::uuid AND l.phase='scan' AND fresh.id=sqlc.arg(run_id)::uuid AND fresh.root_id=l.root_id
 AND (l.run_id IS NULL OR l.run_id=fresh.id OR EXISTS(SELECT 1 FROM scan_run old WHERE old.id=l.run_id AND old.state IN('partial','failed')))
 AND NOT EXISTS(SELECT 1 FROM library_scan_job other WHERE other.run_id=fresh.id AND other.job_id<>l.job_id);

-- name: SetNextLibraryScanDue :exec
UPDATE library_scan_schedule SET next_run_at=clock_timestamp()+interval '5 minutes' WHERE root_id=$1;

-- name: InsertLibraryMissingJob :exec
WITH inserted AS (
 INSERT INTO job(id,kind,state,payload,priority,max_attempts)
 VALUES(sqlc.arg(job_id)::uuid,'library_missing','queued',jsonb_build_object('root_id',sqlc.arg(root_id)::uuid,'run_id',sqlc.arg(run_id)::uuid),15,5)
 RETURNING id
)
INSERT INTO library_scan_job(job_id,root_id,phase,run_id) SELECT id,sqlc.arg(root_id)::uuid,'missing',sqlc.arg(run_id)::uuid FROM inserted;

-- name: HasQualifiedLibraryMissing :one
SELECT EXISTS(SELECT 1 FROM qualified_scan_missing WHERE root_id=$1 AND last_run_id=$2);

-- name: RecoverExpiredLibraryScanJobs :execrows
UPDATE job SET state=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'queued' END,
lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE kind IN('library_scan','library_missing') AND state='running' AND lease_until<=clock_timestamp();
