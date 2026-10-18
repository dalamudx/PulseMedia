-- name: HasLibraryReuseProtection :one
SELECT (EXISTS(SELECT 1 FROM unit_reservation remote WHERE remote.root_id=sqlc.arg(root_id) AND remote.unit_id=sqlc.arg(unit_id) AND remote.state IN ('reserved','dispatched')) OR EXISTS(SELECT 1 FROM library_reuse_unit local WHERE local.root_id=sqlc.arg(root_id) AND local.unit_id=sqlc.arg(unit_id) AND local.active AND local.plan_id<>sqlc.arg(ignore_plan)))::boolean AS protected;

-- name: CreateLibraryReusePlan :one
INSERT INTO library_reuse_plan(id,actor_id,root_id,source_file_id,job_id,action,target_rel,frozen)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;

-- name: ReserveLibraryReuseUnit :exec
INSERT INTO library_reuse_unit(plan_id,root_id,unit_id) VALUES($1,$2,$3);

-- name: GetLibraryReusePlan :one
SELECT * FROM library_reuse_plan WHERE id=$1;

-- name: GetLibraryReusePlanForJob :one
SELECT * FROM library_reuse_plan WHERE job_id=$1;

-- name: ListLibraryReusePlans :many
SELECT * FROM library_reuse_plan WHERE actor_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: GuardLibraryReuseJob :one
WITH locked AS MATERIALIZED (SELECT * FROM job WHERE id=sqlc.arg(id) FOR UPDATE)
SELECT id FROM locked WHERE kind='library_reuse' AND state='running'
AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp();

-- name: SetLibraryReuseState :exec
UPDATE library_reuse_plan SET state=$2,reason=$3,updated_at=clock_timestamp() WHERE id=$1 AND state<>'completed';

-- name: CompleteLibraryReuse :one
UPDATE library_reuse_plan SET state='completed',reason='',target_file_id=$2,updated_at=clock_timestamp()
WHERE id=$1 AND state='running' RETURNING *;

-- name: ConsumeLibraryReuseUnits :exec
UPDATE library_reuse_unit SET active=false WHERE plan_id=$1;

-- name: ListLibraryReuseUnits :many
SELECT * FROM library_reuse_unit WHERE plan_id=$1 ORDER BY unit_id;

-- name: FreezeLibraryReuseContent :exec
INSERT INTO library_reuse_content(plan_id,content) VALUES($1,$2);

-- name: GetLibraryReuseContent :one
SELECT content FROM library_reuse_content WHERE plan_id=$1;

-- name: RecoverLibraryReuseJobs :execrows
UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE kind='library_reuse' AND state='running' AND lease_until<=clock_timestamp();

-- name: WaitLibraryReuseJob :exec
UPDATE job SET state='queued',not_before=clock_timestamp()+interval '30 seconds',attempts=GREATEST(attempts-1,0),lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE id=$1;

-- name: ReviewLibraryReuseJob :exec
UPDATE job SET state='dead',error='local reuse requires review',lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1;

-- name: LockLibraryReusePolicy :one
SELECT * FROM policy WHERE id=$1 FOR UPDATE;

-- name: LockLibraryReuseSubscription :one
SELECT * FROM subscription WHERE id=$1 FOR UPDATE;
