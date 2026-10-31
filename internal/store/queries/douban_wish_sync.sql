-- name: ListDueDoubanWishConfigs :many
SELECT c.id,c.owner_id,c.version FROM douban_sync_config c
JOIN app_user u ON u.id=c.owner_id
WHERE c.enabled AND u.is_active AND u.role IN ('admin','member')
AND (sqlc.arg(after_id)::uuid='00000000-0000-0000-0000-000000000000'::uuid
 OR (c.owner_id,c.id)>(sqlc.arg(after_owner_id)::uuid,sqlc.arg(after_id)::uuid))
AND NOT EXISTS(SELECT 1 FROM douban_wish_sync_run r WHERE r.config_id=c.id AND r.status IN ('queued','running','review_required'))
AND NOT EXISTS(SELECT 1 FROM douban_wish_sync_run r WHERE r.config_id=c.id AND r.config_version=c.version AND r.finished_at>clock_timestamp()-interval '24 hours')
ORDER BY c.owner_id,c.id LIMIT 64;

-- name: DoubanWishPhysicalLockHeld :one
SELECT pulse_douban_wish_permit_held(sqlc.arg(lock_key)::bigint,sqlc.arg(execution_pid)::int)::boolean;

-- name: CreateDoubanWishJob :exec
INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,$2,'queued',$3,1);

-- name: InsertDoubanWishPage :exec
INSERT INTO douban_wish_sync_page(job_id,run_id,media_type,page_start) VALUES($1,$2,$3,$4);

-- name: DoubanWishConfigDue :one
SELECT NOT EXISTS(SELECT 1 FROM douban_wish_sync_run WHERE config_id=$1 AND (status IN ('queued','running','review_required') OR (config_version=$2 AND finished_at>clock_timestamp()-interval '24 hours')));

-- name: InsertDoubanWishRun :exec
INSERT INTO douban_wish_sync_run(id,config_id,config_version,owner_id,status) VALUES($1,$2,$3,$4,'queued');

-- name: UnlockDoubanWishPermit :one
SELECT pg_advisory_unlock($1::bigint)::boolean;

-- name: GetDoubanWishConfigID :one
SELECT r.config_id FROM douban_wish_sync_page p JOIN douban_wish_sync_run r ON r.id=p.run_id WHERE p.job_id=$1;

-- name: TryDoubanWishPermit :one
SELECT pg_try_advisory_lock($1::bigint)::boolean;

-- name: RegisterDoubanWishExecution :execrows
UPDATE douban_wish_sync_page SET execution_id=$2,execution_pid=$3,execution_owner=$4,execution_token=$5 WHERE job_id=$1 AND execution_id IS NULL;

-- name: StartDoubanWishRun :exec
UPDATE douban_wish_sync_run SET status='running' WHERE id=$1;

-- name: LockDoubanWishJob :one
SELECT * FROM job WHERE id=$1 FOR UPDATE;

-- name: GetDoubanWishBinding :one
SELECT r.id,r.config_id,r.owner_id,r.config_version,p.media_type,p.page_start FROM douban_wish_sync_page p JOIN douban_wish_sync_run r ON r.id=p.run_id WHERE p.job_id=$1;

-- name: LockDoubanWishAccount :one
SELECT is_active,role,username FROM app_user WHERE id=$1 FOR UPDATE;

-- name: LockDoubanWishRun :one
SELECT status FROM douban_wish_sync_run WHERE id=$1 FOR UPDATE;

-- name: LockDoubanWishPage :one
SELECT * FROM douban_wish_sync_page WHERE job_id=$1 FOR UPDATE;

-- name: DoubanWishLeaseTail :one
SELECT kind=sqlc.arg(kind)::text AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp() AND (state='running' OR (sqlc.arg(completed)::boolean AND state='succeeded')) FROM job WHERE id=sqlc.arg(id)::uuid;

-- name: GetDoubanWishCanonical :one
SELECT canonical_id,profile_number FROM douban_wish_sync_run WHERE id=$1;

-- name: GetDoubanWishPage :one
SELECT page FROM douban_wish_sync_page WHERE job_id=$1;

-- name: SaveDoubanWishPage :exec
UPDATE douban_wish_sync_page SET page=$2 WHERE job_id=$1;

-- name: DoubanWishTotalChanged :one
SELECT EXISTS(SELECT 1 FROM douban_wish_sync_page WHERE run_id=$1 AND media_type=$2 AND page IS NOT NULL AND (page->>'Total')::int<>sqlc.arg(total)::int);

-- name: BindDoubanWishCanonical :exec
UPDATE douban_wish_sync_run SET canonical_id=coalesce(canonical_id,sqlc.arg(canonical_id)::text),profile_number=coalesce(profile_number,nullif(sqlc.arg(profile_number)::text,'')),incomplete=incomplete OR sqlc.arg(incomplete)::boolean WHERE id=sqlc.arg(id)::uuid;

-- name: InsertDoubanWishItem :execrows
INSERT INTO douban_wish_sync_item(id,run_id,page_job_id,douban_id,media_type,source,source_hash,command_key,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending') ON CONFLICT(run_id,media_type,douban_id) DO NOTHING;

-- name: MarkDoubanWishIncomplete :exec
UPDATE douban_wish_sync_run SET incomplete=true WHERE id=$1;

-- name: ListDoubanWishPageItems :many
SELECT id,source,state,command_key FROM douban_wish_sync_item WHERE page_job_id=$1 ORDER BY douban_id;

-- name: LockDoubanWishItem :one
SELECT source,match,state FROM douban_wish_sync_item WHERE id=$1 AND page_job_id=$2 FOR UPDATE;

-- name: SaveDoubanWishMatch :exec
UPDATE douban_wish_sync_item SET match=$2,match_hash=$3,state=$4 WHERE id=$1;

-- name: GetDoubanWishItemState :one
SELECT state FROM douban_wish_sync_item WHERE id=$1 AND page_job_id=$2 FOR UPDATE;

-- name: FinishDoubanWishItem :exec
UPDATE douban_wish_sync_item SET state=$2 WHERE id=$1;

-- name: DoubanWishPagePending :one
SELECT EXISTS(SELECT 1 FROM douban_wish_sync_item WHERE page_job_id=$1 AND state IN ('pending','matched'));

-- name: FinishDoubanWishPage :exec
UPDATE douban_wish_sync_page SET finished=true WHERE job_id=$1;

-- name: FinishDoubanWishRun :exec
UPDATE douban_wish_sync_run SET status=CASE WHEN incomplete OR EXISTS(SELECT 1 FROM douban_wish_sync_item WHERE run_id=$1 AND state<>'created') THEN 'partial' ELSE 'succeeded' END,finished_at=clock_timestamp() WHERE id=$1;

-- name: CompleteDoubanWishJob :exec
UPDATE job SET state='succeeded',result=(SELECT jsonb_build_object('observed',count(*),'created',count(*) FILTER(WHERE state='created')) FROM douban_wish_sync_item WHERE page_job_id=$1),finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1;

-- name: LockExpiredDoubanWishJobs :many
SELECT j.id FROM job j JOIN douban_wish_sync_page p ON p.job_id=j.id JOIN douban_wish_sync_run r ON r.id=p.run_id WHERE r.status IN ('queued','running') AND j.kind=$1 AND j.state IN ('running','dead','failed') AND (j.state<>'running' OR j.lease_until<=clock_timestamp()) ORDER BY j.id LIMIT 64 FOR UPDATE OF j SKIP LOCKED;

-- name: DeadDoubanWishJob :exec
UPDATE job SET state='dead',error='douban wish observation requires review',finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state<>'succeeded';

-- name: ReviewDoubanWishRun :exec
UPDATE douban_wish_sync_run SET status='review_required' WHERE id=(SELECT run_id FROM douban_wish_sync_page WHERE job_id=$1) AND status IN ('queued','running');
