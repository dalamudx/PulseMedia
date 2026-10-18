-- These reads run in the selection account-first repeatable-read transaction.
-- name: ListTVSelectionWanted :many
SELECT w.unit_id,w.id,w.is_skipped,w.is_satisfied,COALESCE(w.current_fact_stage,'')::text AS fact_stage,
 (p.inputs_current AND (w.projection_valid_until IS NULL OR w.projection_valid_until>clock_timestamp()))::boolean AS current,
 (EXISTS(SELECT 1 FROM unit_reservation r WHERE r.unit_id=w.unit_id AND r.state IN ('reserved','dispatched')) OR EXISTS(SELECT 1 FROM acquisition_reservation a WHERE a.unit_id=w.unit_id AND a.released_at IS NULL))::boolean AS protected,
 w.evaluated_contract_version,w.evaluated_write_token,w.evaluated_facts_version,w.projection_valid_until,w.next_evaluate_at
FROM wanted_item w JOIN wanted_projection_validity p ON p.id=w.id
WHERE w.subscription_id=$1 ORDER BY w.unit_id;

-- name: ListTVSelectionLocalReuseProtection :many
SELECT DISTINCT w.unit_id FROM wanted_item w
JOIN library_reuse_unit local ON local.unit_id=w.unit_id AND local.active
WHERE w.subscription_id=$1;
-- name: GetTVSelectionCandidateObservationWindow :one
SELECT (min(q.finished_at)+interval '30 minutes')::timestamptz AS first_valid_until,
       (max(q.finished_at)+interval '30 minutes')::timestamptz AS last_valid_until
FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id
JOIN release_candidate c ON c.id=qc.candidate_id AND c.indexer_id=q.indexer_id AND c.source_config_version=q.config_version
WHERE c.id=sqlc.arg(candidate_id) AND q.state='succeeded'
AND q.finished_at>clock_timestamp()-interval '30 minutes';

