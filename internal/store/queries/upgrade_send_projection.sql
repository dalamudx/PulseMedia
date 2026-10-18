-- These queries defer only derived projection writes while a confirmed one-shot
-- SEND owns the frozen Wanted observation. They grant no execution permission.
-- name: HasMovieUpgradeSendProjectionSchema :one
SELECT (to_regclass('movie_upgrade_send_job') IS NOT NULL)::boolean AS installed;

-- name: IsMovieUpgradeSendProjectionDeferred :one
SELECT (EXISTS (
 SELECT 1 FROM wanted_item w
 JOIN movie_upgrade_authority a ON a.subscription_id=w.subscription_id AND a.unit_id=w.unit_id
 JOIN movie_upgrade_send_job b USING(operation_id)
 JOIN job j ON j.id=b.job_id
 WHERE w.id=$1 AND NOT w.is_skipped AND NOT w.scope_excluded AND a.status='reserved_not_executable'
 AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND b.protected_sha256=pulse_upgrade_send_job_protected(a.operation_id)
 AND pulse_is_upgrade_send_job(j)
 AND ((j.state='queued' AND j.attempts=0)
   OR (j.state='running' AND j.attempts=1 AND j.lease_until>clock_timestamp()))
) OR EXISTS (
 SELECT 1 FROM wanted_item w
 JOIN movie_upgrade_authority a ON a.subscription_id=w.subscription_id AND a.unit_id=w.unit_id
 JOIN movie_upgrade_preparation_lease l USING(operation_id)
 WHERE w.id=$1 AND NOT w.is_skipped AND NOT w.scope_excluded
 AND a.status='reserved_not_executable' AND l.actor_id=a.actor_id
 AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND l.state='leased' AND l.lease_until>clock_timestamp()
) OR EXISTS (
 SELECT 1 FROM wanted_item w
 JOIN movie_upgrade_authority a ON a.subscription_id=w.subscription_id AND a.unit_id=w.unit_id
 JOIN movie_upgrade_preparation p USING(operation_id)
 JOIN movie_upgrade_preparation_lease l USING(operation_id)
 JOIN download_task t ON t.id=p.task_id AND t.operation_id=a.operation_id
 JOIN subscription s ON s.id=a.subscription_id
 JOIN app_user u ON u.id=a.actor_id
 WHERE w.id=$1
 AND NOT w.is_skipped AND NOT w.scope_excluded
 AND a.status='reserved_not_executable' AND p.status='prepared_not_sent'
 AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND p.actor_id=a.actor_id AND l.actor_id=a.actor_id
 AND l.state='prepared' AND l.owner=p.owner AND l.token=p.token
 AND p.task_snapshot=to_jsonb(t)
 AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND a.snapshot->>'mode'=s.upgrade_mode
 AND s.intent_version=(a.snapshot->'observation'->>'intent_version')::bigint
 AND u.is_active AND u.role='admin'
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_send_job b WHERE b.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_send_authorization x WHERE x.operation_id=a.operation_id)
) OR EXISTS (
 SELECT 1 FROM wanted_item waiting
 JOIN movie_upgrade_authority a ON a.subscription_id=waiting.subscription_id AND a.unit_id=waiting.unit_id
 JOIN subscription s ON s.id=a.subscription_id
 JOIN app_user u ON u.id=a.actor_id
 JOIN acquisition_reservation lane ON lane.operation_id=a.operation_id AND lane.unit_id=a.unit_id
 JOIN unit_reservation reservation ON reservation.operation_id=a.operation_id AND reservation.unit_id=a.unit_id
 JOIN download_requirement demand ON demand.operation_id=a.operation_id AND demand.unit_id=a.unit_id
 WHERE waiting.id=$1 AND NOT waiting.is_skipped AND NOT waiting.scope_excluded
 AND a.status='reserved_not_executable' AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND a.snapshot->>'mode'=s.upgrade_mode AND s.intent_version=a.intent_version
 AND s.root_id=a.root_id AND s.quality_policy_id=a.policy_id AND s.quality_policy_revision=a.policy_revision
 AND u.is_active AND u.role='admin'
 AND lane.released_at IS NULL AND lane.task_id IS NULL AND lane.candidate_id=a.candidate_id
 AND lane.source_root_id=a.root_id AND lane.source_generation=a.root_generation
 AND reservation.purpose='upgrade' AND reservation.state='reserved' AND reservation.root_id=a.root_id
 AND reservation.candidate_id=a.candidate_id AND (reservation.expires_at IS NULL OR reservation.expires_at>clock_timestamp())
 AND demand.state='pending' AND demand.released_at IS NULL AND demand.root_id=a.root_id AND demand.actor_id=a.actor_id::text
 AND demand.submission_fence->>'goal'='upgrade' AND demand.submission_fence->>'authority'=a.operation_id::text
 AND demand.submission_fence->>'protected_digest'=a.snapshot->>'protected_digest'
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_preparation_lease l WHERE l.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_preparation p WHERE p.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM download_task t WHERE t.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM job j WHERE j.payload->>'operation_id'=a.operation_id::text)
))::boolean AS deferred;
