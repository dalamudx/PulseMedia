-- Account is locked first by LockAcquisitionCommandUser; policy precedes subscription/root/unit.
-- This transaction takes shared locks but never writes business rows.
-- name: LockUpgradePolicy :one
SELECT * FROM policy WHERE id=$1 FOR SHARE;

-- name: LockUpgradeSubscription :one
SELECT * FROM subscription WHERE id=$1 FOR SHARE;

-- name: LockUpgradeRoot :one
SELECT * FROM library_root WHERE id=$1 FOR SHARE;

-- name: ListUpgradeUnits :many
SELECT * FROM media_unit WHERE media_id=$1 ORDER BY id FOR SHARE;

-- Include all active versions, even those without imported facts.
-- name: ListUpgradeFiles :many
SELECT f.*, (SELECT count(*) FROM library_file_unit x WHERE x.file_id=f.id AND x.retired_at IS NULL)::bigint AS unit_count
FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id AND fu.retired_at IS NULL
WHERE f.root_id=$1 AND fu.unit_id=$2 AND f.retired_at IS NULL ORDER BY f.id FOR SHARE OF f,fu;

-- Capture original expiry without evaluating it against an earlier clock sample.
-- name: ListUpgradeFacts :many
SELECT * FROM unit_fact WHERE source_file_id=$1 AND unit_id=$2 AND root_id=$3 AND stage='imported' AND revoked_at IS NULL ORDER BY id FOR SHARE;

-- Last potentially blocking read. No time predicate may extend this proof.
-- name: LockUpgradeCandidate :one
SELECT * FROM release_candidate WHERE id=$1 FOR SHARE;

-- name: ReadUpgradeSourceWindow :one
SELECT (max(q.finished_at)+interval '30 minutes')::timestamptz AS valid_until
FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id
JOIN release_candidate c ON c.id=qc.candidate_id AND c.indexer_id=q.indexer_id AND c.source_config_version=q.config_version
WHERE c.id=$1 AND q.state='succeeded';
