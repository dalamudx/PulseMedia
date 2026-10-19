-- name: LibraryOverviewSnapshotTime :one
SELECT transaction_timestamp()::timestamptz AS observed_at;

-- name: CountLibraryOverview :one
SELECT count(DISTINCT m.id) FILTER (WHERE m.media_type='movie')::text AS movies,
 count(DISTINCT m.id) FILTER (WHERE m.media_type='tv')::text AS tv_shows,
 count(DISTINCT u.id) FILTER (WHERE m.media_type='tv')::text AS episodes,
 count(DISTINCT f.id)::text AS files
FROM library_file f
JOIN library_file_unit mapping ON mapping.file_id=f.id AND mapping.retired_at IS NULL
JOIN media_unit u ON u.id=mapping.unit_id AND NOT u.retired
JOIN media m ON m.id=u.media_id
WHERE f.retired_at IS NULL AND EXISTS (
 SELECT 1 FROM unit_fact fact WHERE fact.source_file_id=f.id AND fact.root_id=f.root_id
 AND fact.unit_id=u.id AND fact.stage='imported' AND fact.revoked_at IS NULL
 AND (fact.expires_at IS NULL OR fact.expires_at>transaction_timestamp())
);

-- name: ListLibraryOverviewRoots :many
WITH observations AS (
 SELECT r.id AS root_id,r.name,r.media_type,r.health,
 COALESCE(r.health='online' AND r.baseline_completed
  AND latest.state='succeeded' AND latest.generation=r.generation AND latest.scan_token=r.scan_token
  AND latest.finished_at IS NOT NULL AND final.completed_at IS NOT NULL AND final.exhausted
  AND final.root_id=r.id AND final.expected_path=r.path AND final.expected_media_type=r.media_type
  AND final.expected_marker_filename=r.marker_filename
  AND baseline.expected_path=r.path AND baseline.expected_media_type=r.media_type
  AND baseline.expected_marker_filename=r.marker_filename
  AND final.root_device=baseline.root_device AND final.root_inode=baseline.root_inode,false) AS available,
 capacity.total_bytes,capacity.available_bytes,capacity.sampled_at
 FROM library_root r
 LEFT JOIN LATERAL (
  SELECT run.* FROM scan_run run WHERE run.root_id=r.id ORDER BY run.created_at DESC,run.id DESC LIMIT 1
 ) latest ON true
 LEFT JOIN scan_finalization final ON final.scan_run_id=latest.id
 LEFT JOIN root_capacity_observation capacity ON capacity.scan_run_id=latest.id
 LEFT JOIN LATERAL (
  SELECT sealed.* FROM scan_run run JOIN scan_finalization sealed ON sealed.scan_run_id=run.id
  WHERE run.root_id=r.id AND sealed.root_id=r.id AND run.generation=r.generation
   AND run.state='succeeded' AND sealed.completed_at IS NOT NULL
  ORDER BY run.created_at,run.id LIMIT 1
 ) baseline ON true
), valid AS (
 SELECT *,available AND sampled_at<=transaction_timestamp() AND isfinite(sampled_at)
 AND total_bytes>0 AND available_bytes>=0 AND available_bytes<=total_bytes AS valid_sample
 FROM observations
)
SELECT root_id,name,media_type,health,
 CASE WHEN available THEN 'available' ELSE 'unknown' END::text AS availability,
 CASE WHEN valid_sample THEN CASE WHEN sampled_at>=transaction_timestamp()-interval '30 minutes' THEN 'fresh' ELSE 'stale' END ELSE 'unknown' END::text AS capacity_state,
 COALESCE((CASE WHEN valid_sample THEN total_bytes END)::text,'') AS total_bytes,
 COALESCE((CASE WHEN valid_sample THEN available_bytes END)::text,'') AS available_bytes,
 (CASE WHEN valid_sample THEN sampled_at END)::timestamptz AS sampled_at
FROM valid ORDER BY name,root_id;

-- name: ListLibraryOverviewRecent :many
-- Arrival belongs to the physical file/root history, even after a mapping correction.
WITH first_import AS (
 SELECT source_file_id,root_id,min(created_at) AS imported_at FROM unit_fact
 WHERE stage='imported' GROUP BY source_file_id,root_id
), recent AS (
 SELECT m.id,max(first_import.imported_at) AS imported_at
 FROM library_file f
 JOIN first_import ON first_import.source_file_id=f.id AND first_import.root_id=f.root_id
 JOIN library_file_unit mapping ON mapping.file_id=f.id AND mapping.retired_at IS NULL
 JOIN media_unit u ON u.id=mapping.unit_id AND NOT u.retired
 JOIN media m ON m.id=u.media_id
 WHERE f.retired_at IS NULL AND EXISTS (
  SELECT 1 FROM unit_fact fact WHERE fact.source_file_id=f.id AND fact.root_id=f.root_id
   AND fact.unit_id=u.id AND fact.stage='imported' AND fact.revoked_at IS NULL
   AND (fact.expires_at IS NULL OR fact.expires_at>transaction_timestamp())
 )
 GROUP BY m.id ORDER BY imported_at DESC,m.id DESC LIMIT sqlc.arg(recent_limit)::integer
)
SELECT m.id AS media_id,m.media_type,m.title,m.year,recent.imported_at::timestamptz AS imported_at,
 CASE WHEN artwork.provider='tmdb' AND artwork.namespace='tmdb.'||m.media_type
  AND artwork.metadata_version=m.metadata_version
  AND artwork.external_id ~ '^[1-9][0-9]{0,18}$'
  AND (SELECT count(*) FROM media_identity identity WHERE identity.media_id=m.id AND identity.namespace='tmdb.'||m.media_type)=1
  AND EXISTS (SELECT 1 FROM media_identity identity WHERE identity.media_id=m.id
   AND identity.namespace=artwork.namespace AND identity.external_id=artwork.external_id)
 THEN artwork.poster_path ELSE '' END::text AS poster_path
FROM recent JOIN media m ON m.id=recent.id
LEFT JOIN media_artwork artwork ON artwork.media_id=m.id
ORDER BY recent.imported_at DESC,m.id DESC;