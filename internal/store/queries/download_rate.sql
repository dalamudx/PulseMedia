-- name: DownloadRateTaskSource :one
-- Classification only. The caller owns execution authority and lease validation.
SELECT (CASE
 WHEN EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=$1::uuid) THEN 'automatic_tv'
 WHEN EXISTS(SELECT 1 FROM complete_tv_execution_authority WHERE operation_id=$1::uuid) THEN 'complete_tv'
 WHEN EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=$1::uuid) THEN 'movie_upgrade'
 ELSE 'ordinary' END)::text AS source;

-- name: UpsertDownloadRateObservation :exec
INSERT INTO download_rate_observation(task_id,client_id,task_stamp,task_updated_at,client_stamp,source,download_rate_bytes_per_second,observed_progress,observed_at)
VALUES(sqlc.arg(task_id)::uuid,sqlc.arg(client_id)::uuid,sqlc.arg(task_stamp)::text,sqlc.arg(task_updated_at)::timestamptz,sqlc.arg(client_stamp)::text,sqlc.arg(source)::text,sqlc.narg(download_rate_bytes_per_second)::bigint,sqlc.narg(observed_progress)::double precision,clock_timestamp())
ON CONFLICT(task_id) DO UPDATE SET client_id=EXCLUDED.client_id,task_stamp=EXCLUDED.task_stamp,
 task_updated_at=EXCLUDED.task_updated_at,client_stamp=EXCLUDED.client_stamp,source=EXCLUDED.source,
 download_rate_bytes_per_second=EXCLUDED.download_rate_bytes_per_second,observed_progress=EXCLUDED.observed_progress,observed_at=EXCLUDED.observed_at;

-- name: ListOperationDownloadRates :many
-- One clock for the entire batch, from the caller's authorized read snapshot.
WITH snapshot AS MATERIALIZED (SELECT clock_timestamp()::timestamptz AS snapshot_at)
SELECT sqlc.embed(dt),sqlc.embed(dc),
 coalesce(r.task_stamp,'')::text AS task_stamp,r.task_updated_at,
 coalesce(r.client_stamp,'')::text AS client_stamp,coalesce(r.source,'')::text AS source,
 r.download_rate_bytes_per_second,r.observed_progress,r.observed_at,snapshot.snapshot_at,
 (CASE WHEN EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=dt.operation_id) THEN 'automatic_tv'
 WHEN EXISTS(SELECT 1 FROM complete_tv_execution_authority WHERE operation_id=dt.operation_id) THEN 'complete_tv'
 WHEN EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=dt.operation_id) THEN 'movie_upgrade'
 ELSE 'ordinary' END)::text AS current_source
FROM download_task dt JOIN download_client dc ON dc.id=dt.client_id
CROSS JOIN snapshot LEFT JOIN download_rate_observation r ON r.task_id=dt.id AND r.client_id=dc.id
WHERE dt.operation_id=ANY(sqlc.arg(operation_ids)::uuid[]);
