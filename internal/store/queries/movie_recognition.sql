-- name: LockMovieRecognitionRoots :many
SELECT r.id FROM library_root r
WHERE r.media_type='movie' AND r.health='online' AND r.baseline_completed
 AND EXISTS(SELECT 1 FROM library_file f WHERE f.root_id=r.id AND f.retired_at IS NULL AND f.size_bytes>=sqlc.arg(minimum_media_bytes)::bigint
 AND f.file_fingerprint IS NOT NULL AND f.file_fingerprint<>''
 AND NOT EXISTS(SELECT 1 FROM library_file_unit m WHERE m.file_id=f.id AND m.retired_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM movie_recognition_work w WHERE w.file_id=f.id AND w.root_generation=r.generation))
 AND (sqlc.narg(after_root_id)::uuid IS NULL OR r.id>sqlc.narg(after_root_id)::uuid)
ORDER BY r.id LIMIT sqlc.arg(max_roots)::int FOR UPDATE OF r SKIP LOCKED;

-- name: ListMovieRecognitionFiles :many
-- Fresh statement after acquiring the root lock; never rely on its old snapshot.
SELECT f.id,r.generation,f.rel_path,f.size_bytes FROM library_file f JOIN library_root r ON r.id=f.root_id
WHERE r.id=sqlc.arg(root_id)::uuid AND r.media_type='movie' AND r.health='online' AND r.baseline_completed
 AND (sqlc.narg(after_id)::uuid IS NULL OR f.id>sqlc.narg(after_id)::uuid)
 AND f.size_bytes>=sqlc.arg(minimum_media_bytes)::bigint
 AND f.retired_at IS NULL AND f.file_fingerprint IS NOT NULL AND f.file_fingerprint<>''
 AND NOT EXISTS(SELECT 1 FROM library_file_unit m WHERE m.file_id=f.id AND m.retired_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM movie_recognition_work w WHERE w.file_id=f.id AND w.root_generation=r.generation)
ORDER BY f.id LIMIT 25 FOR UPDATE OF f SKIP LOCKED;

-- name: InsertMovieRecognitionJob :exec
WITH inserted AS (
 INSERT INTO job(id,kind,state,payload,priority,max_attempts)
 VALUES(sqlc.arg(job_id)::uuid,'movie_recognition','queued',jsonb_build_object('root_id',sqlc.arg(root_id)::uuid,'file_id',sqlc.arg(file_id)::uuid),10,5) RETURNING id
)
INSERT INTO movie_recognition_work(job_id,root_id,file_id,root_generation)
SELECT id,sqlc.arg(root_id)::uuid,sqlc.arg(file_id)::uuid,sqlc.arg(generation)::bigint FROM inserted;

-- name: GetMovieRecognitionWork :one
SELECT w.*,f.rel_path FROM movie_recognition_work w JOIN library_file f ON f.id=w.file_id
 JOIN job j ON j.id=w.job_id WHERE w.job_id=$1 AND j.kind='movie_recognition';

-- name: GuardOwnedMovieRecognitionWork :one
WITH locked AS MATERIALIZED (
 SELECT j.id,j.kind,j.state,j.lease_owner,j.lease_token,j.lease_until,w.root_id,w.file_id,w.root_generation,w.outcome
 FROM job j JOIN movie_recognition_work w ON w.job_id=j.id WHERE j.id=sqlc.arg(job_id)::uuid FOR UPDATE OF j
)
SELECT id FROM locked WHERE kind='movie_recognition' AND state='running' AND outcome='pending'
 AND root_id=sqlc.arg(root_id)::uuid AND file_id=sqlc.arg(file_id)::uuid AND root_generation=sqlc.arg(generation)::bigint
 AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp();

-- name: FinishMovieRecognitionWork :execrows
UPDATE movie_recognition_work SET outcome=sqlc.arg(outcome)::text,reason=sqlc.narg(reason)::text,
unit_id=sqlc.narg(unit_id)::uuid,finished_at=clock_timestamp() WHERE job_id=$1 AND outcome='pending';

-- name: InsertMovieRecognitionAudit :exec
INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload,created_at)
VALUES($1,'system','movie_recognizer','file_identification','library_file',$2,$3,clock_timestamp());

-- name: RecoverExpiredMovieRecognitionJobs :execrows
UPDATE job SET state=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'queued' END,
lease_owner=NULL,lease_until=NULL,updated_at=clock_timestamp()
WHERE kind='movie_recognition' AND state='running' AND lease_until<=clock_timestamp();

-- name: BeginMovieFileCheck :one
SELECT s.id AS scan_id,f.root_device,f.root_inode,clock_timestamp()::timestamptz AS checked_at
FROM library_root r JOIN scan_run s ON s.root_id=r.id JOIN scan_finalization f ON f.scan_run_id=s.id
WHERE r.id=sqlc.arg(root_id)::uuid AND r.health='online' AND r.media_type='movie' AND r.baseline_completed
 AND s.state='succeeded' AND s.generation=sqlc.arg(generation)::bigint AND s.generation=r.generation
 AND f.completed_health_revision IS NOT NULL AND f.exhausted AND f.completed_at IS NOT NULL
 AND f.expected_path=r.path AND f.expected_media_type=r.media_type AND f.expected_marker_filename=r.marker_filename
ORDER BY f.completed_at DESC,s.id DESC LIMIT 1;

-- name: GuardMovieFileCheck :one
SELECT EXISTS(SELECT 1 FROM library_root r JOIN scan_run s ON s.root_id=r.id JOIN scan_finalization f ON f.scan_run_id=s.id
 WHERE r.id=sqlc.arg(root_id)::uuid AND r.health='online' AND r.media_type='movie' AND r.baseline_completed
 AND s.id=sqlc.arg(scan_id)::uuid AND s.state='succeeded' AND s.generation=sqlc.arg(generation)::bigint AND s.generation=r.generation
 AND f.completed_health_revision IS NOT NULL AND f.exhausted AND f.completed_at IS NOT NULL
 AND f.expected_path=r.path AND f.expected_media_type=r.media_type AND f.expected_marker_filename=r.marker_filename
 AND f.root_device=sqlc.arg(root_device)::bigint AND f.root_inode=sqlc.arg(root_inode)::bigint
 AND sqlc.arg(checked_at)::timestamptz<=clock_timestamp() AND sqlc.arg(checked_at)::timestamptz>clock_timestamp()-interval '5 seconds')::bool;
