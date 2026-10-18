-- name: ListTVMediaSubscriptions :many
SELECT * FROM subscription WHERE media_id=$1 ORDER BY id;
-- name: ApplyTVEpisodeMetadata :one
INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number,absolute_number,air_date,retired,facts_version,metadata_version) VALUES($1,$2,$3,$4,$5,$6,$7,false,1,1)
ON CONFLICT(media_id,unit_key) DO UPDATE SET season_number=EXCLUDED.season_number,episode_number=EXCLUDED.episode_number,absolute_number=EXCLUDED.absolute_number,air_date=EXCLUDED.air_date,retired=false,updated_at=clock_timestamp() RETURNING *;
-- name: RetireMissingTVSeasonUnits :exec
UPDATE media_unit SET retired=true,updated_at=clock_timestamp() WHERE media_id=$1 AND season_number=$2 AND NOT (episode_number=ANY($3::integer[])) AND NOT retired;
-- name: AdvanceTVMediaMetadataVersion :one
UPDATE media SET metadata_version=metadata_version+1,updated_at=clock_timestamp() WHERE id=$1 AND media_type='tv' RETURNING *;
