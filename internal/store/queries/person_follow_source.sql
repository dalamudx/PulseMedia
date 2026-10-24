-- name: ReadPersonFollowSource :one
SELECT * FROM person_follow_sync_source WHERE run_id=$1;

-- name: InsertPersonFollowSource :exec
INSERT INTO person_follow_sync_source(run_id,execution_id,source,source_hash,work_count)VALUES($1,$2,$3,$4,$5);

-- name: InsertPersonFollowItems :execrows
INSERT INTO person_follow_sync_item(id,run_id,ordinal,tmdb_id,media_type,source,root_id,command_key,state,writer_execution_id)
SELECT x.id,x.run_id,x.ordinal,x.tmdb_id,x.media_type,x.source,x.root_id,x.command_key,x.state,x.writer_execution_id
FROM jsonb_to_recordset($1::jsonb) AS x(id UUID,run_id UUID,ordinal INTEGER,tmdb_id BIGINT,media_type TEXT,source JSONB,root_id UUID,command_key TEXT,state TEXT,writer_execution_id UUID);

-- name: ListPersonFollowPageItems :many
SELECT * FROM person_follow_sync_item WHERE run_id=sqlc.arg(run_id)::uuid AND ordinal>=sqlc.arg(page_start)::int AND ordinal<sqlc.arg(page_start)::int+64 ORDER BY ordinal;

-- name: ReviewPersonFollowItem :execrows
UPDATE person_follow_sync_item SET state='review_required',writer_execution_id=$3 WHERE id=$1 AND run_id=$2 AND state='pending';

-- name: LockPersonFollowItem :one
SELECT * FROM person_follow_sync_item WHERE id=$1 FOR UPDATE;

-- name: CompletePersonFollowSyncPage :exec
UPDATE person_follow_sync_page SET finished=true WHERE job_id=$1;

-- name: CompletePersonFollowSyncJob :exec
UPDATE job SET state='succeeded',result=jsonb_build_object('page_completed',true),finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1;

-- name: CompletePersonFollowSyncRun :exec
UPDATE person_follow_sync_run SET status=CASE WHEN EXISTS(SELECT 1 FROM person_follow_sync_item WHERE run_id=$1 AND state='review_required') THEN 'partial' ELSE 'succeeded' END,finished_at=clock_timestamp() WHERE id=$1;
