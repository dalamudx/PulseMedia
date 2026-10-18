-- name: InsertEpisodeMappingRevision :one
INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,group_id,part_id,input_stamp,source_hash,source_snapshot,mappings,valid_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $6 IN ('group','absolute') THEN clock_timestamp()+interval '6 hours' ELSE NULL END) RETURNING *;
-- name: AdvanceEpisodeMappingBinding :one
UPDATE subscription SET episode_mapping_revision=$2,intent_version=intent_version+1,updated_at=clock_timestamp() WHERE id=$1 AND intent_version=$3 AND COALESCE(episode_mapping_revision,0)=$4 RETURNING *;
-- name: InvalidateEpisodeMappingWanted :exec
UPDATE wanted_item SET intent_version=$2,next_evaluate_at=clock_timestamp(),updated_at=clock_timestamp() WHERE subscription_id=$1;
-- name: GetCurrentEpisodeMapping :one
SELECT r.* FROM subscription s JOIN episode_mapping_revision r ON r.subscription_id=s.id AND r.revision=s.episode_mapping_revision WHERE s.id=$1;
