-- name: GetSiteAccountStatsReadSource :one
SELECT config_version FROM indexer WHERE id=$1 FOR SHARE;

-- name: GetSiteAccountStatsReadClock :one
SELECT clock_timestamp()::timestamptz AS observed_now;

-- name: GetSiteAccountStatsLatestSuccess :one
SELECT sample, config_version, source_version, finished_at
FROM site_account_stats_attempt
WHERE indexer_id=$1 AND state='succeeded'
ORDER BY finished_at DESC, job_id DESC LIMIT 1;

-- name: GetSiteAccountStatsLatestAttempt :one
SELECT started_at, state, failure_code
FROM site_account_stats_attempt
WHERE indexer_id=$1
ORDER BY created_at DESC, job_id DESC LIMIT 1;
