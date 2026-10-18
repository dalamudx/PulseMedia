-- name: MatchesMovieUpgradeAuthority :one
SELECT actor_id = sqlc.arg(actor_id)
  AND subscription_id = sqlc.arg(subscription_id)
  AND intent_version = sqlc.arg(intent_version)
  AND unit_id = sqlc.arg(unit_id)
  AND root_id = sqlc.arg(root_id)
  AND root_generation = sqlc.arg(root_generation)
  AND old_file_id = sqlc.arg(old_file_id)
  AND old_fact_id = sqlc.arg(old_fact_id)
  AND candidate_id = sqlc.arg(candidate_id)
  AND client_id = sqlc.arg(client_id)
  AND policy_id = sqlc.arg(policy_id)
  AND policy_revision = sqlc.arg(policy_revision)
  AND snapshot = sqlc.arg(snapshot)::jsonb
  AND goal = 'upgrade'
  AND status = 'reserved_not_executable'
FROM movie_upgrade_authority
WHERE operation_id = sqlc.arg(operation_id);
