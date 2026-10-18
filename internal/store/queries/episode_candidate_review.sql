-- name: CreateEpisodeCandidateReview :one
INSERT INTO episode_candidate_review(id,actor_id,subscription_id,media_id,unit_id,candidate_id,input_stamp,unit_key,unit_version,media_version,intent_version,mapping_revision,policy_revision,root_generation,source_version,parser_version,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING *;
-- name: GetEpisodeCandidateReview :one
SELECT * FROM episode_candidate_review WHERE id=$1;
