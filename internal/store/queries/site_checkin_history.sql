-- name: GetSiteCheckinHistorySource :one
SELECT id FROM indexer WHERE id=$1 FOR SHARE;

-- name: ListSiteCheckinHistory :many
SELECT id, indexer_id, expected_account_id, profile, timezone,
       calendar_day::text AS calendar_day, day_start, day_end, status, safe_code,
       observed_at, (prepared_execution_id IS NOT NULL)::boolean AS prepared,
       config_version, source_version, (restore_id IS NOT NULL)::boolean AS restore_acknowledged
FROM site_checkin_day
WHERE indexer_id=sqlc.arg(indexer_id)::uuid
  AND (NOT sqlc.arg(has_cursor)::boolean OR
       (calendar_day,id)<(sqlc.arg(cursor_day)::text::date,sqlc.arg(cursor_id)::uuid))
ORDER BY calendar_day DESC,id DESC
LIMIT sqlc.arg(page_limit)::integer;
