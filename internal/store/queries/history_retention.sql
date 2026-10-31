-- name: SetHistoryRetentionBudget :exec
SET LOCAL statement_timeout = '2s';

-- Only superseded, completed observations are disposable. Current, last-good,
-- active and uncertain attempts keep their original execution evidence.
-- name: PruneGlobalRateObservations :many
WITH expired AS (
 SELECT a.job_id FROM download_global_rate_attempt a JOIN job j ON j.id=a.job_id
 WHERE a.state IN ('succeeded','failed') AND a.finished_at<transaction_timestamp()-interval '14 days'
 AND j.kind='download_global_rates' AND j.state='succeeded' AND j.finished_at<transaction_timestamp()-interval '14 days'
 AND (j.lease_until IS NULL OR j.lease_until<=transaction_timestamp())
 AND NOT EXISTS (SELECT 1 FROM download_global_rate_schedule s WHERE a.job_id IN (s.active_job_id,s.latest_job_id,s.last_good_job_id))
 ORDER BY a.finished_at,a.job_id LIMIT 200 FOR UPDATE OF a,j SKIP LOCKED
)
DELETE FROM download_global_rate_attempt a USING expired e WHERE a.job_id=e.job_id RETURNING a.job_id;

-- name: PruneGlobalRateJobs :execrows
DELETE FROM job WHERE id=ANY(sqlc.arg(job_ids)::uuid[]) AND kind='download_global_rates' AND state='succeeded'
 AND finished_at<transaction_timestamp()-interval '14 days'
 AND NOT EXISTS(SELECT 1 FROM download_global_rate_attempt a WHERE a.job_id=job.id);

-- Proven query links remain immutable; only truly unreferenced candidates expire.
-- name: PruneUnreferencedReleaseCandidates :execrows
WITH expired AS (
 SELECT c.id FROM release_candidate c WHERE c.created_at<transaction_timestamp()-interval '30 days'
 AND NOT EXISTS(SELECT 1 FROM indexer_query_candidate r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM unit_reservation r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM acquisition_reservation r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM download_task r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM search_round r WHERE r.selected_candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM episode_candidate_review r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM episode_execution_authority r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM release_block r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM tv_coverage_candidate_review r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM complete_tv_execution_authority r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_authority r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_query_candidate r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_decision r WHERE r.candidate_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_authority r WHERE r.candidate_id=c.id)
 ORDER BY c.created_at,c.id LIMIT 200 FOR UPDATE OF c SKIP LOCKED
)
DELETE FROM release_candidate c USING expired e WHERE c.id=e.id;
