-- name: GetLeasedSearchEndpointScope :one
SELECT r.decision_snapshot,r.deadline,r.state,i.config_version,i.health,j.lease_until,
 (r.state='running' AND r.deadline>clock_timestamp())::boolean AS before_deadline
FROM job j JOIN search_round r ON r.id=sqlc.arg(round_id)
JOIN search_round_endpoint e ON e.round_id=r.id AND e.indexer_id=sqlc.arg(indexer_id)
JOIN indexer i ON i.id=e.indexer_id
WHERE j.id=sqlc.arg(job_id) AND j.kind='search_endpoint' AND j.state='running'
 AND j.lease_owner=sqlc.arg(owner) AND j.lease_token=sqlc.arg(token)
 AND j.lease_until>clock_timestamp() AND e.state='pending'
 AND j.payload->>'round_id'=r.id::text AND j.payload->>'indexer_id'=i.id::text
 AND j.dedupe_key=r.id::text||':'||i.id::text;
