-- name: IsSubscriptionReleaseBlocked :one
SELECT EXISTS(SELECT 1 FROM release_block WHERE subscription_id=$1 AND candidate_id=$2 AND blocked) AS blocked;
-- name: SetSubscriptionReleaseBlock :exec
INSERT INTO release_block(subscription_id,candidate_id,blocked) VALUES($1,$2,$3)
ON CONFLICT(subscription_id,candidate_id) DO UPDATE SET blocked=EXCLUDED.blocked,updated_at=clock_timestamp();
-- name: CountSubscriptionReleaseBlocks :one
SELECT count(*) FROM release_block WHERE subscription_id=$1 AND blocked;
-- name: ListSubscriptionBlockedCandidates :many
SELECT c.* FROM release_candidate c JOIN release_block b ON b.candidate_id=c.id WHERE b.subscription_id=$1 AND b.blocked ORDER BY c.id LIMIT 100;
-- name: HasSubscriptionCandidateEvidence :one
SELECT EXISTS(SELECT 1 FROM release_block b WHERE b.subscription_id=sqlc.arg(subscription_id)::uuid AND b.candidate_id=sqlc.arg(candidate_id)::uuid) OR EXISTS(
 SELECT 1 FROM search_round r WHERE r.subscription_id=sqlc.arg(subscription_id)::uuid AND (
 r.candidate_ids @> jsonb_build_array(sqlc.arg(candidate_id)::uuid::text) OR EXISTS(
 SELECT 1 FROM indexer_query_candidate qc WHERE qc.candidate_id=sqlc.arg(candidate_id)::uuid AND (
 EXISTS(SELECT 1 FROM search_round_endpoint e WHERE e.round_id=r.id AND e.query_id=qc.query_id) OR
 EXISTS(SELECT 1 FROM search_round_rss_query rq WHERE rq.round_id=r.id AND rq.query_id=qc.query_id))))) AS visible;
