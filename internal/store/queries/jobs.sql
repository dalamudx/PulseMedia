-- name: EnqueueJob :one
INSERT INTO job (
    id, kind, dedupe_key, priority, state, payload, max_attempts, not_before, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, 'queued', $5, $6, $7, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: ClaimJob :one
WITH candidate AS (
    SELECT id FROM job
    WHERE state = 'queued' AND not_before <= statement_timestamp()
      AND kind = ANY($1::text[]) AND attempts < max_attempts AND $3::integer > 0
    -- Bound priority starvation without rewriting priorities or bypassing readiness.
    -- Aged ready work precedes fresh work; equal readiness keeps priority ordering.
    ORDER BY CASE WHEN not_before <= statement_timestamp() - interval '5 minutes' THEN 0 ELSE 1 END,
             CASE WHEN not_before <= statement_timestamp() - interval '5 minutes' THEN not_before END,
             priority DESC, not_before, id
    LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE job AS j
SET state = 'running', lease_owner = $2::text,
    lease_until = clock_timestamp() + ($3::integer * interval '1 millisecond'),
    lease_token = j.lease_token + 1, attempts = j.attempts + 1,
    updated_at = clock_timestamp()
FROM candidate AS c
WHERE j.id = c.id AND j.state = 'queued'
RETURNING j.*;

-- name: LockJobForWrite :one
SELECT id FROM job WHERE id = $1 FOR UPDATE;

-- name: FinishJob :one
WITH locked AS MATERIALIZED (
    SELECT * FROM job WHERE id = $1::uuid FOR UPDATE
), authorized AS MATERIALIZED (
    SELECT id FROM locked WHERE state = 'running'
      AND lease_owner = $2::text AND lease_token = $3::bigint
      AND lease_until > clock_timestamp()
)
UPDATE job
SET state = 'succeeded', finished_at = clock_timestamp(),
    updated_at = clock_timestamp(), lease_owner = NULL, lease_until = NULL,
    result = $4::jsonb
FROM authorized WHERE job.id = authorized.id
RETURNING job.*;

-- name: FailJob :one
WITH locked AS MATERIALIZED (
    SELECT * FROM job WHERE id = $1::uuid FOR UPDATE
), authorized AS MATERIALIZED (
    SELECT id FROM locked WHERE state = 'running'
      AND lease_owner = $2::text AND lease_token = $3::bigint
      AND lease_until > clock_timestamp() AND $5::integer > 0
)
UPDATE job
SET state = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'queued' END,
    error = $4::text,
    not_before = clock_timestamp() + ($5::integer * interval '1 millisecond'),
    lease_owner = NULL, lease_until = NULL,
    updated_at = clock_timestamp()
FROM authorized WHERE job.id = authorized.id
RETURNING job.*;

-- name: RenewJobLease :one
WITH locked AS MATERIALIZED (
    SELECT * FROM job WHERE id = $1::uuid FOR UPDATE
), authorized AS MATERIALIZED (
    SELECT id FROM locked WHERE state = 'running'
      AND lease_owner = $2::text AND lease_token = $3::bigint
      AND lease_until > clock_timestamp() AND $4::integer > 0
)
UPDATE job
SET lease_until = clock_timestamp() + ($4::integer * interval '1 millisecond'),
    updated_at = clock_timestamp()
FROM authorized WHERE job.id = authorized.id
RETURNING job.*;

-- name: RecoverExpiredDownloadJobs :execrows
-- Requeue only fenced download submissions. The worker observes any persisted task
-- before calling Add; a stale lease alone never authorizes replay.
UPDATE job SET state = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'queued' END,
    lease_owner = NULL, lease_until = NULL, updated_at = statement_timestamp()
WHERE kind IN ('download_submission','episode_download_submission') AND state = 'running' AND lease_until <= clock_timestamp();

-- name: GetJobByID :one
SELECT * FROM job WHERE id = $1;

-- name: GuardDownloadSubmissionCommit :one
WITH locked AS MATERIALIZED (
    SELECT * FROM job WHERE id=sqlc.arg(id) FOR UPDATE
)
SELECT id FROM locked
WHERE kind IN ('download_submission','episode_download_submission') AND state='running'
  AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)
  AND lease_until>clock_timestamp();
