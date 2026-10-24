-- name: ListDownloadGlobalRateSources :many
SELECT c.id,c.name,c.client_type,COALESCE(pulse_global_rates_binding(c.id),'')::text AS config_stamp,
 s.latest_job_id,s.last_good_job_id
FROM download_client c LEFT JOIN download_global_rate_schedule s ON s.client_id=c.id
WHERE c.client_type IN ('qbittorrent','transmission') ORDER BY c.id;

-- name: ListDownloadGlobalRateViewSamples :many
SELECT a.* FROM download_global_rate_attempt a JOIN download_client c ON c.id=a.client_id
WHERE c.client_type IN ('qbittorrent','transmission') AND a.state IN ('succeeded','failed','abandoned')
AND (COALESCE(a.observed_at,a.finished_at)>=sqlc.arg(since)::timestamptz OR a.job_id=ANY(sqlc.arg(related_job_ids)::uuid[]))
ORDER BY COALESCE(a.observed_at,a.finished_at),a.job_id;
