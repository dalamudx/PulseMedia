-- Download Queries

-- name: CreateDownloadClient :one
INSERT INTO download_client (
    id, name, client_type, url, username, password_encrypted, remote_path_map, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetDownloadClientByID :one
SELECT * FROM download_client WHERE id = $1;

-- name: ListDownloadClients :many
SELECT * FROM download_client ORDER BY created_at ASC;

-- name: DeleteDownloadClientIfUnused :execrows
DELETE FROM download_client AS c WHERE c.id = $1
  AND NOT EXISTS (SELECT 1 FROM download_task AS t WHERE t.client_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM subscription AS s WHERE s.downloader_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM job AS j WHERE j.kind = 'download_submission'
      AND j.state IN ('queued', 'running') AND j.payload->>'client_id' = c.id::text);

-- name: CreateDownloadTask :one
INSERT INTO download_task (
    id, operation_id, client_id, candidate_id, info_hash, state, save_path,
    progress, total_bytes, downloaded_bytes, reconcile_count, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, 0, 0, 0, 0, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetDownloadTaskByOpID :one
SELECT * FROM download_task WHERE operation_id = $1;

-- name: GetDownloadTaskByInfoHash :one
SELECT * FROM download_task WHERE info_hash = $1;

-- name: UpdateDownloadTaskProgress :one
UPDATE download_task
SET state = $2, progress = $3, total_bytes = $4, downloaded_bytes = $5,
    content_path = $6, updated_at = statement_timestamp()
WHERE id = $1 AND state = 'tracking' AND $2 = 'tracking'
RETURNING *;

-- name: TransitionTaskToUnknown :one
UPDATE download_task
SET state = 'unknown', error = $2, updated_at = statement_timestamp()
WHERE id = $1 AND state IN ('submitting', 'tracking', 'unknown')
RETURNING *;

-- name: TransitionTaskToTracking :one
UPDATE download_task
SET state = 'tracking', error = NULL, updated_at = statement_timestamp()
WHERE id = $1 AND state IN ('submitting', 'unknown')
RETURNING *;

-- name: LockDownloadTask :one
SELECT * FROM download_task WHERE id=$1 FOR UPDATE;

-- name: LockDownloadTaskByOperation :one
SELECT * FROM download_task WHERE operation_id=$1 FOR UPDATE;

-- name: CompleteDownloadForOrganization :one
UPDATE download_task SET state='completed', progress=1, total_bytes=$2, downloaded_bytes=$3,
 content_path=$4, finished_at=statement_timestamp(), organization_state='queued', updated_at=statement_timestamp()
WHERE id=$1 AND state='tracking' RETURNING *;

-- name: EnqueueDownloadOrganization :one
INSERT INTO job (id,kind,dedupe_key,state,payload,max_attempts,not_before)
VALUES ($1,'download_organization','organize:'||sqlc.arg(task_id)::uuid::text,'queued',
 jsonb_build_object('task_id',sqlc.arg(task_id)::uuid::text),20,statement_timestamp()) RETURNING *;

-- name: GuardDownloadOrganizationJob :one
SELECT id FROM job WHERE id=sqlc.arg(id) AND kind='download_organization' AND state='running'
 AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint
 AND lease_until>clock_timestamp() FOR UPDATE;

-- name: GetDownloadOrganizationInputs :many
SELECT DISTINCT d.unit_id,d.root_id,m.id AS media_id,m.title,m.year,m.media_type,u.retired,u.unit_key,
 c.matched_unit_id,c.quality_profile,r.health,r.baseline_completed,
 COALESCE(a.source_stamp,'')::text AS acquisition_source_stamp,c.title AS candidate_title,COALESCE(c.info_hash,'')::text AS candidate_hash,c.link_encrypted,
 (a.id IS NOT NULL AND a.candidate_id=t.candidate_id AND a.task_id=t.id AND NOT a.legacy_conflict)::boolean AS frozen_coverage,
 NOT EXISTS(SELECT 1 FROM download_requirement x WHERE x.operation_id=t.operation_id AND x.unit_id=d.unit_id AND x.root_id=d.root_id AND x.state='pending' AND x.submission_fence IS NOT NULL AND (
 COALESCE(x.submission_fence->>'candidate_id','')<>c.id::text OR COALESCE(x.submission_fence->>'unit_id','')<>u.id::text OR COALESCE(x.submission_fence->>'root_id','')<>r.id::text OR COALESCE(x.submission_fence->>'root_generation','')<>r.generation::text OR COALESCE(x.submission_fence->>'unit_metadata_version','')<>u.metadata_version::text OR COALESCE(x.submission_fence->>'media_metadata_version','')<>m.metadata_version::text)) AS demand_versions_current
FROM download_task t JOIN download_requirement d ON d.operation_id=t.operation_id
JOIN media_unit u ON u.id=d.unit_id JOIN media m ON m.id=u.media_id
JOIN release_candidate c ON c.id=t.candidate_id JOIN library_root r ON r.id=d.root_id
LEFT JOIN acquisition_reservation a ON a.operation_id=t.operation_id AND a.unit_id=d.unit_id AND a.released_at IS NULL
WHERE t.id=$1 AND d.state='pending' ORDER BY d.root_id,d.unit_id;

-- name: CountPendingOrganizationRequirements :one
SELECT count(*)::bigint FROM download_requirement WHERE operation_id=$1 AND state='pending';

-- name: AttachDownloadTransferPlan :one
UPDATE download_task SET transfer_plan_id=$2, updated_at=statement_timestamp()
WHERE id=$1 AND state='completed' AND organization_state='queued' AND transfer_plan_id IS NULL RETURNING *;

-- name: ReviewDownloadOrganization :one
UPDATE download_task SET organization_state='review_required', organization_reason=$2,updated_at=statement_timestamp()
WHERE id=$1 AND state='completed' AND organization_state='queued' RETURNING *;

-- name: FinishDownloadOrganization :one
UPDATE download_task SET organization_state='completed',organization_reason=NULL,updated_at=statement_timestamp()
WHERE download_task.id=$1 AND transfer_plan_id=$2 AND organization_state='queued'
 AND NOT EXISTS (SELECT 1 FROM download_requirement d WHERE d.operation_id=download_task.operation_id AND d.state='pending')
RETURNING *;

-- name: ContinueDownloadOrganization :one
UPDATE download_task SET transfer_plan_id=NULL,updated_at=clock_timestamp() WHERE id=$1 AND state='completed' AND organization_state='queued' AND transfer_plan_id=$2 RETURNING *;
-- name: ContinueOrganizationJob :one
UPDATE job SET state='queued',attempts=0,not_before=clock_timestamp(),lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp(),result=jsonb_build_object('last_plan_id',sqlc.arg(plan_id)::uuid,'state','remaining_roots')
WHERE id=sqlc.arg(id) AND kind='download_organization' AND state='running' AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp() RETURNING *;
-- name: ReleaseCancelledOrganizationReservations :exec
UPDATE unit_reservation r SET state='released',updated_at=clock_timestamp()
WHERE r.operation_id=$1 AND r.state IN ('reserved','dispatched') AND NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=r.operation_id AND d.root_id=r.root_id AND d.unit_id=r.unit_id AND d.state='pending');

-- name: ImportDownloadRequirements :exec
UPDATE download_requirement SET state='imported' WHERE operation_id=$1 AND unit_id=$2 AND root_id=$3 AND state='pending';

-- name: ReviewDownloadOrganizationJob :one
UPDATE job SET state='dead',error='download organization requires manual review',lease_owner=NULL,lease_until=NULL,updated_at=statement_timestamp()
WHERE id=sqlc.arg(id) AND kind='download_organization' AND state='running'
 AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp()
RETURNING *;

-- name: RecoverExpiredOrganizationJobs :execrows
UPDATE job SET state=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'queued' END,
 lease_owner=NULL,lease_until=NULL,updated_at=statement_timestamp()
WHERE kind='download_organization' AND state='running' AND lease_until<=clock_timestamp();

-- name: SetObservedDownloadInfoHash :one
UPDATE download_task SET info_hash = $2, updated_at = statement_timestamp()
WHERE id = $1 AND info_hash = '' AND state IN ('submitting', 'unknown')
RETURNING *;

-- name: ListTrackingDownloadTasks :many
SELECT * FROM download_task WHERE state = 'tracking'
ORDER BY updated_at ASC LIMIT 20;

-- name: ListPendingDownloadTasks :many
SELECT * FROM download_task WHERE state IN ('submitting', 'unknown')
ORDER BY updated_at ASC LIMIT 20;

-- name: IncrementReconcileCount :one
UPDATE download_task
SET reconcile_count = reconcile_count + 1, updated_at = statement_timestamp()
WHERE id = $1
RETURNING reconcile_count;


-- name: UpdateDownloadClient :one
UPDATE download_client
SET name = $2, client_type = $3, url = $4, username = $5,
    password_encrypted = COALESCE($6, password_encrypted),
    updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;
