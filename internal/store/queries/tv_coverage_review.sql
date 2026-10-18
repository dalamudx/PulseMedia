-- name: CreateTVCoverageCandidateReview :one
INSERT INTO tv_coverage_candidate_review(id,actor_id,subscription_id,candidate_id,input_stamp,material_stamp,material,facts,resolved_units,reason)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING *;

-- name: GetTVCoverageCandidateReview :one
SELECT * FROM tv_coverage_candidate_review WHERE id=$1;

-- name: GetTVCoverageSourceEvidence :one
SELECT COALESCE(jsonb_agg(jsonb_build_object('query_id',q.id,'config_version',q.config_version,'finished_at',q.finished_at,'expires_at',q.finished_at+interval '30 minutes') ORDER BY q.id),'[]'::jsonb)::jsonb AS evidence
FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id
WHERE q.indexer_id=sqlc.arg(indexer_id) AND q.config_version=sqlc.arg(config_version) AND q.state='succeeded' AND qc.candidate_id=sqlc.arg(candidate_id)
AND q.finished_at>clock_timestamp()-interval '30 minutes';
