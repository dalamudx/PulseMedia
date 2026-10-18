-- name: ListSearchEndpointPages :many
SELECT p.*, ep.page_offset,ep.page_limit,ep.item_count,ep.offset_verified,ep.total
FROM search_round_endpoint_page p JOIN indexer_query_page ep ON ep.query_id=p.query_id
WHERE p.round_id=$1 AND p.indexer_id=$2 ORDER BY p.page;

-- name: AddSearchEndpointPage :exec
INSERT INTO search_round_endpoint_page(round_id,indexer_id,page,query_id,source_job_id,source_token) VALUES($1,$2,$3,$4,$5,$6);

-- name: YieldSearchEndpointPageJob :one
UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL,not_before=GREATEST(clock_timestamp(),sqlc.arg(not_before)::timestamptz),attempts=GREATEST(attempts-1,0),updated_at=clock_timestamp()
WHERE id=sqlc.arg(job_id) AND state='running' AND lease_owner=sqlc.arg(owner) AND lease_token=sqlc.arg(token) AND lease_until>clock_timestamp()
RETURNING id;

-- name: IsSearchEndpointLeaseTimeValid :one
SELECT sqlc.arg(lease_until)::timestamptz > clock_timestamp() AS valid;

