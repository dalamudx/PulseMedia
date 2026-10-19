-- name: RecheckSubscriptionEpisode :execrows
-- A manual observation request does not change scope, intent, retry gates or protection.
UPDATE wanted_item AS w
SET next_evaluate_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE w.subscription_id = sqlc.arg(subscription_id)
  AND w.unit_id = sqlc.arg(unit_id)
  AND w.intent_version = sqlc.arg(intent_version)
  AND NOT w.is_skipped AND NOT w.user_skipped AND NOT w.scope_excluded
  AND EXISTS (
      SELECT 1 FROM media_unit AS u
      JOIN media AS m ON m.id = u.media_id
      WHERE u.id = w.unit_id AND NOT u.retired AND m.media_type = 'tv'
  );
