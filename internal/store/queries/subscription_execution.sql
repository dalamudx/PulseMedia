-- name: ReadSubscriptionExecutionFacts :one
WITH rounds AS (
 SELECT id,'movie'::text AS kind,state,created_at,deadline,snapshot_intent_version AS intent_version,COALESCE(abort_reason,'') AS reason,selected_candidate_id
 FROM search_round WHERE subscription_id=$1
 UNION ALL
 SELECT r.id,'tv',r.state,r.created_at,r.deadline,(r.snapshot->>'intent_version')::bigint,COALESCE(d.decision->>'reason',''),d.candidate_id
 FROM automatic_tv_round r LEFT JOIN automatic_tv_decision d ON d.round_id=r.id WHERE r.subscription_id=$1
), recent AS (SELECT * FROM rounds ORDER BY created_at DESC,id DESC LIMIT 21)
SELECT COALESCE(jsonb_agg(jsonb_build_object('id',r.id,'kind',r.kind,'state',r.state,'created_at',r.created_at,'deadline',r.deadline,'intent_version',r.intent_version,'reason',r.reason,
 'sources',COALESCE((SELECT jsonb_agg(jsonb_build_object('indexer_id',s.indexer_id,'state',s.state,'pages',COALESCE((
  SELECT jsonb_agg(jsonb_build_object('query_id',q.id,'page',q.page,'state',q.state,'results',q.results_count,'receipt_present',p.query_id IS NOT NULL,'offset_verified',COALESCE(p.offset_verified,false),'finished_at',q.finished_at) ORDER BY q.page,q.id)
  FROM indexer_query q LEFT JOIN indexer_query_page p ON p.query_id=q.id WHERE q.id IN (
   SELECT query_id FROM search_round_endpoint e WHERE e.round_id=r.id AND e.indexer_id=s.indexer_id
   UNION SELECT query_id FROM search_round_endpoint_page e WHERE e.round_id=r.id AND e.indexer_id=s.indexer_id
   UNION SELECT query_id FROM search_round_rss_query e WHERE e.round_id=r.id AND q.indexer_id=s.indexer_id
   UNION SELECT query_id FROM automatic_tv_query e WHERE e.round_id=r.id AND e.indexer_id=s.indexer_id
  )), '[]'::jsonb)) ORDER BY s.indexer_id)
 FROM (SELECT indexer_id,state FROM search_round_endpoint WHERE round_id=r.id UNION ALL SELECT indexer_id,state FROM automatic_tv_source WHERE round_id=r.id
 UNION ALL SELECT q.indexer_id,CASE WHEN bool_and(q.state='succeeded') THEN 'returned' WHEN bool_or(q.state='running') THEN 'pending' ELSE 'failed' END FROM search_round_rss_query rq JOIN indexer_query q ON q.id=rq.query_id WHERE rq.round_id=r.id AND NOT EXISTS(SELECT 1 FROM search_round_endpoint e WHERE e.round_id=r.id AND e.indexer_id=q.indexer_id) GROUP BY q.indexer_id) s),'[]'::jsonb),
 'candidates',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'outcome',CASE WHEN c.id=r.selected_candidate_id THEN 'selected' WHEN c.blocked THEN 'rejected' WHEN r.state IN ('running') THEN 'pending' ELSE 'not_selected' END,'reason',CASE WHEN c.id=r.selected_candidate_id THEN 'selected' WHEN c.blocked THEN 'subscription_blocked' WHEN r.state='running' THEN 'awaiting_decision' ELSE 'no_persisted_rejection' END) ORDER BY c.id)
 FROM (SELECT DISTINCT ids.id,EXISTS(SELECT 1 FROM release_block b WHERE b.subscription_id=$1 AND b.candidate_id=ids.id AND b.blocked) AS blocked FROM (
 SELECT qc.candidate_id AS id FROM indexer_query_candidate qc WHERE qc.query_id IN (
 SELECT query_id FROM search_round_endpoint WHERE round_id=r.id UNION SELECT query_id FROM search_round_endpoint_page WHERE round_id=r.id UNION SELECT query_id FROM search_round_rss_query WHERE round_id=r.id UNION SELECT query_id FROM automatic_tv_query WHERE round_id=r.id)
 UNION SELECT value::uuid FROM search_round sr CROSS JOIN LATERAL jsonb_array_elements_text(sr.candidate_ids) WHERE sr.id=r.id
 ) ids ORDER BY ids.id LIMIT 101) c),'[]'::jsonb)
) ORDER BY r.created_at DESC,r.id DESC),'[]'::jsonb)::jsonb AS body FROM recent r;
