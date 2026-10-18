-- name: CreateTVSeasonMetadataSnapshot :exec
INSERT INTO tv_season_metadata_snapshot
 (media_id, media_version, season_number, source_tmdb_id, units, content_hash)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListTVSeasonMetadataSnapshots :many
SELECT * FROM tv_season_metadata_snapshot
WHERE media_id=$1 AND media_version=$2
ORDER BY season_number;
