-- name: BindTVDBSeriesIdentity :execrows
INSERT INTO tvdb_series_identity(media_id,tmdb_series_id,tvdb_series_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING;

-- name: GetTVDBSeriesIdentity :one
SELECT * FROM tvdb_series_identity WHERE media_id=$1;

-- name: BindTVDBEpisodeEvidence :execrows
INSERT INTO tvdb_episode_evidence(unit_id,media_id,tmdb_series_id,tvdb_series_id,tmdb_episode_id,tvdb_episode_id,season_number,episode_number,credential_version,fallback_version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING;

-- name: GetTVDBEpisodeEvidence :one
SELECT * FROM tvdb_episode_evidence WHERE unit_id=$1;
