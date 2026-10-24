-- Policy Queries

-- name: GetPolicyByID :one
SELECT * FROM policy WHERE id = $1;

-- name: ListPolicies :many
SELECT * FROM policy ORDER BY id ASC;

-- name: CreatePolicy :one
INSERT INTO policy (
    id, name, current_revision, created_at, updated_at
) VALUES (
    $1, $2, 1, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetPolicyRevision :one
SELECT * FROM policy_revision
WHERE policy_id = $1 AND revision = $2;

-- name: InsertPolicyRevision :one
INSERT INTO policy_revision (
    id, policy_id, revision, rules, created_at
) VALUES (
    $1, $2, $3, $4, statement_timestamp()
) RETURNING *;

-- name: BumpPolicyCurrentRevision :one
UPDATE policy
SET current_revision = current_revision + 1, updated_at = statement_timestamp()
WHERE id = $1
RETURNING current_revision;

-- name: UpdateSubscriptionsPolicyRevision :exec
UPDATE subscription
SET quality_policy_revision = $2, intent_version = intent_version + 1, updated_at = statement_timestamp()
WHERE quality_policy_id = $1;

-- name: InvalidateWantedItemsForPolicy :exec
UPDATE wanted_item
SET next_evaluate_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE subscription_id IN (
    SELECT id FROM subscription WHERE quality_policy_id = $1
);

-- name: ListPolicyDocuments :many
SELECT p.id,p.name,p.current_revision,r.rules FROM policy p JOIN policy_revision r ON r.policy_id=p.id AND r.revision=p.current_revision ORDER BY p.id;

-- name: GetInteractiveRankingPolicy :one
-- Scope authorization precedes this immutable revision read in the same RR snapshot.
SELECT r.policy_id, r.revision, r.rules
FROM policy p
JOIN policy_revision r ON r.policy_id=p.id
LEFT JOIN subscription s ON s.id=sqlc.narg(subscription_id)::uuid
WHERE p.id=COALESCE(s.quality_policy_id,'default')
 AND r.revision=COALESCE(s.quality_policy_revision,p.current_revision);
