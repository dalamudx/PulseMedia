-- name: ListRecoverablePersonFollowJobs :many
SELECT j.id,r.id AS run_id,r.config_id,r.owner_id FROM job j JOIN person_follow_sync_page p ON p.job_id=j.id JOIN person_follow_sync_run r ON r.id=p.run_id
JOIN person_follow_config c ON c.id=r.config_id JOIN app_user u ON u.id=r.owner_id
WHERE j.id>sqlc.arg(after_id)::uuid AND j.kind='person_follow_sync' AND r.status IN ('queued','running') AND NOT p.finished AND (
 (j.state='running' AND j.lease_until<=clock_timestamp()) OR j.state IN ('dead','failed','succeeded','cancelled')
 OR (j.state='queued' AND (j.attempts>=j.max_attempts OR NOT c.enabled OR c.version<>r.config_version OR NOT u.is_active OR u.role NOT IN ('admin','member')
 OR NOT EXISTS(SELECT 1 FROM policy WHERE id=c.quality_policy_id AND current_revision=c.quality_policy_revision)
 OR (c.movie_root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_root WHERE id=c.movie_root_id AND health='online'))
 OR (c.tv_root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_root WHERE id=c.tv_root_id AND health='online')))))
ORDER BY j.id LIMIT 64;

-- name: TryPersonFollowRecoveryLock :one
SELECT pg_try_advisory_xact_lock(pulse_person_follow_lock_key($1::uuid))::boolean;

-- name: RecoverPersonFollowJob :execrows
UPDATE job SET state=sqlc.arg(next_state)::text,lease_owner=NULL,lease_until=NULL,not_before=greatest(not_before,clock_timestamp()),error=sqlc.arg(reason)::text,
finished_at=CASE WHEN sqlc.arg(next_state)::text='dead' THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp()
WHERE id=sqlc.arg(id)::uuid AND kind='person_follow_sync' AND state=sqlc.arg(previous_state)::text AND lease_token=sqlc.arg(lease_token)::bigint AND (state<>'running' OR lease_until<=clock_timestamp());

-- name: RecoverPersonFollowRun :execrows
UPDATE person_follow_sync_run SET status=sqlc.arg(next_state)::text,finished_at=CASE WHEN sqlc.arg(next_state)::text='aborted' THEN clock_timestamp() ELSE NULL END
WHERE id=sqlc.arg(id)::uuid AND status IN ('queued','running');
