-- SearchRound Queries

-- name: CreateSearchRound :one
INSERT INTO search_round (
    id, subscription_id, wanted_item_id, snapshot_intent_version,
    snapshot_policy_revision, state, deadline, created_at
) VALUES (
    $1, $2, $3, $4, $5, 'running', $6, statement_timestamp()
) RETURNING *;

-- name: GetSearchRoundByID :one
SELECT * FROM search_round WHERE id = $1;

-- name: GetActiveSearchRoundForWanted :one
SELECT * FROM search_round
WHERE wanted_item_id = $1 AND state = 'running'
LIMIT 1;

-- name: AddCandidateToRound :one
UPDATE search_round
SET candidate_ids = candidate_ids || $2::jsonb
WHERE id = $1
RETURNING *;

-- name: AbortSearchRound :one
UPDATE search_round
SET state = 'aborted', abort_reason = $2, finished_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: CompleteSearchRound :one
UPDATE search_round
SET state = 'completed', selected_candidate_id = $2, finished_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: ExpireSearchRound :one
UPDATE search_round
SET state = 'expired', finished_at = statement_timestamp()
WHERE id = $1
RETURNING *;
