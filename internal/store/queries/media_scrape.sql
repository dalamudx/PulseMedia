-- name: LockMediaScrapeSubmission :exec
SELECT pg_advisory_xact_lock(734077);

-- name: LockMediaScrapeUser :one
SELECT role,is_active FROM app_user WHERE id=$1 FOR SHARE;

-- name: GuardMediaScrapeIsolation :one
SELECT isolated FROM restore_isolation WHERE singleton FOR SHARE;

-- name: LockMediaScrapeRoots :many
SELECT id FROM library_root ORDER BY id FOR SHARE;

-- name: ListMediaScrapeCandidates :many
SELECT * FROM media_scrape_candidate ORDER BY file_id,unit_id;

-- name: CreateMediaScrapeBatch :one
INSERT INTO media_scrape_batch(id,command_key,requested_by) VALUES($1,$2,$3) RETURNING *;

-- name: FindMediaScrapeBatch :one
SELECT id FROM media_scrape_batch WHERE requested_by=$1 AND command_key=$2;

-- name: LatestMediaScrapeBatch :one
SELECT id FROM media_scrape_batch ORDER BY created_at DESC,id DESC LIMIT 1;

-- A single set statement selects and enqueues under the original transaction.
-- The native worker revalidates the entire current file/root/mapping snapshot.
-- name: EnqueueMediaScrapeItems :one
WITH candidates AS MATERIALIZED (
 SELECT c.*,row_number() OVER(ORDER BY file_id,unit_id)::int AS ordinal FROM media_scrape_candidate c WHERE status IN ('missing','complete')
), jobs AS (
 INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before)
 SELECT (sqlc.arg(job_ids)::uuid[])[c.ordinal],'media_sidecar','media-sidecar-rescan:'||sqlc.arg(batch_id)::uuid::text||':'||c.source_event_id::text,
 20,'queued',jsonb_build_object('version',1,'source_event_id',c.source_event_id,'file_id',c.file_id,'unit_id',c.unit_id),1,clock_timestamp()
 FROM candidates c RETURNING id,payload
), items AS (
 INSERT INTO media_scrape_item(batch_id,job_id,file_id,unit_id,source_event_id)
 SELECT sqlc.arg(batch_id)::uuid,j.id,(j.payload->>'file_id')::uuid,(j.payload->>'unit_id')::uuid,(j.payload->>'source_event_id')::uuid FROM jobs j RETURNING 1
)
SELECT count(*)::bigint FROM items;

-- name: ReadMediaScrapeBatch :one
SELECT b.id,b.command_key,b.created_at,count(j.id)::bigint AS total,
 count(j.id) FILTER(WHERE j.state='queued')::bigint AS queued,
 count(j.id) FILTER(WHERE j.state='running')::bigint AS running,
 count(j.id) FILTER(WHERE j.state='succeeded')::bigint AS succeeded,
 count(j.id) FILTER(WHERE j.state NOT IN ('queued','running','succeeded'))::bigint AS review
FROM media_scrape_batch b LEFT JOIN media_scrape_item i ON i.batch_id=b.id LEFT JOIN job j ON j.id=i.job_id
WHERE b.id=$1 GROUP BY b.id;

-- name: AuditMediaScrapeBatch :exec
INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload)
VALUES($1,'user',$2,'media.scrape.queued','media_scrape_batch',$3,jsonb_build_object('total',sqlc.arg(total)::bigint));
