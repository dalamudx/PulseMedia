-- name: InsertIndexerQueryPage :exec
INSERT INTO indexer_query_page(query_id,page_offset,page_limit,item_count,offset_verified,total)
VALUES($1,$2,$3,$4,$5,$6);
-- name: IsIndexerQueryComplete :one
SELECT EXISTS(SELECT 1 FROM indexer_query_page p JOIN indexer_query q ON q.id=p.query_id
 WHERE p.query_id=$1 AND q.state='succeeded' AND p.page_offset=0 AND p.offset_verified
 AND p.total IS NOT NULL AND p.item_count=p.total) AS complete;
-- name: LockIndexerQueryExecution :one
SELECT * FROM indexer_query WHERE id=$1 FOR UPDATE;

-- name: GetCurrentIndexerQuery :one
SELECT q.* FROM indexer_query q JOIN indexer i ON i.id=q.indexer_id
WHERE q.id=$1 AND q.state='succeeded' AND i.config_version=q.config_version AND i.health='online';

-- name: GetIndexerQueryPage :one
SELECT * FROM indexer_query_page WHERE query_id=$1;

-- name: GetSearchContinuationDelay :one
SELECT GREATEST(0,ceil(extract(epoch FROM (GREATEST(next_query_at,COALESCE(rate_limit_until,next_query_at))-clock_timestamp()))*1000))::bigint AS delay_ms
FROM indexer WHERE id=$1 AND config_version=$2 AND health='online';


