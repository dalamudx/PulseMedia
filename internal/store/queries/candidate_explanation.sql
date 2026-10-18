-- name: GetCandidateRuleCache :one
SELECT decision FROM candidate_rule_cache WHERE input_digest=$1;
-- name: PutCandidateRuleCache :exec
INSERT INTO candidate_rule_cache(input_digest,decision) VALUES($1,$2) ON CONFLICT(input_digest) DO NOTHING;
-- name: GetLatestCandidateRound :one
SELECT * FROM search_round WHERE subscription_id=$1 AND wanted_item_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1;
-- name: ListExplainRoundCandidates :many
SELECT DISTINCT c.* FROM release_candidate c WHERE c.id IN (
 SELECT qc.candidate_id FROM indexer_query_candidate qc JOIN indexer_query q ON q.id=qc.query_id JOIN search_round_endpoint e ON e.query_id=q.id WHERE e.round_id=$1 AND q.state='succeeded'
 UNION SELECT qc.candidate_id FROM indexer_query_candidate qc JOIN search_round_rss_query rq ON rq.query_id=qc.query_id WHERE rq.round_id=$1
 UNION SELECT value::uuid FROM search_round r CROSS JOIN LATERAL jsonb_array_elements_text(r.candidate_ids) WHERE r.id=$1)
ORDER BY c.id LIMIT 101;
-- name: HasCandidateRoundTimelyEvidence :one
SELECT EXISTS(SELECT 1 FROM search_round r WHERE r.id=$1 AND (((NOT COALESCE((r.decision_snapshot->>'scheduled')::boolean,false) OR COALESCE((r.decision_snapshot->>'shared_source')::boolean,false)) AND r.candidate_ids @> jsonb_build_array($2::text)) OR EXISTS(
 SELECT 1 FROM indexer_query_candidate qc JOIN release_candidate c ON c.id=qc.candidate_id JOIN indexer_query q ON q.id=qc.query_id WHERE c.id=$2::text::uuid AND q.state='succeeded' AND q.finished_at<=r.deadline AND q.config_version=c.source_config_version AND q.config_version=(r.decision_snapshot->'indexers'->>q.indexer_id::text)::bigint AND (EXISTS(SELECT 1 FROM search_round_endpoint e WHERE e.round_id=r.id AND e.query_id=q.id) OR EXISTS(SELECT 1 FROM search_round_rss_query rq WHERE rq.round_id=r.id AND rq.query_id=q.id))))) AS timely;

