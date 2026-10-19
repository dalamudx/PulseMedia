-- name: InsertRootCapacityObservation :execrows
INSERT INTO root_capacity_observation(scan_run_id,total_bytes,available_bytes,sampled_at)
SELECT run.id,sqlc.arg(total_bytes)::bigint,sqlc.arg(available_bytes)::bigint,sqlc.arg(sampled_at)::timestamptz
FROM scan_run run
WHERE run.id=sqlc.arg(scan_run_id)::uuid AND run.state='running'
 AND sqlc.arg(total_bytes)::bigint>0 AND sqlc.arg(available_bytes)::bigint>=0
 AND sqlc.arg(available_bytes)::bigint<=sqlc.arg(total_bytes)::bigint
 AND sqlc.arg(sampled_at)::timestamptz>=run.created_at
 AND sqlc.arg(sampled_at)::timestamptz<=clock_timestamp();

-- name: RootCapacityObservationMatches :one
SELECT EXISTS(SELECT 1 FROM root_capacity_observation
 WHERE scan_run_id=sqlc.arg(scan_run_id)::uuid AND total_bytes=sqlc.arg(total_bytes)::bigint
 AND available_bytes=sqlc.arg(available_bytes)::bigint
 AND sampled_at=sqlc.arg(sampled_at)::timestamptz)::boolean;
