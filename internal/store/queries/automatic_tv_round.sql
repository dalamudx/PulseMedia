-- name: AutomaticTVReadRound :one
SELECT to_jsonb(r)::jsonb AS body FROM automatic_tv_round r WHERE id=$1;
-- name: AutomaticTVLockRound :one
SELECT to_jsonb(r)::jsonb AS body FROM automatic_tv_round r WHERE id=$1 FOR UPDATE;
-- name: AutomaticTVReadSource :one
SELECT to_jsonb(s)::jsonb AS body FROM automatic_tv_source s WHERE round_id=$1 AND indexer_id=$2;
-- name: AutomaticTVLockSource :one
SELECT to_jsonb(s)::jsonb AS body FROM automatic_tv_source s WHERE round_id=$1 AND indexer_id=$2 FOR UPDATE;
-- name: AutomaticTVReadPages :one
SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.page),'[]'::jsonb)::jsonb AS body FROM automatic_tv_query a WHERE round_id=$1 AND indexer_id=$2;
-- name: AutomaticTVJobValid :one
SELECT EXISTS(SELECT 1 FROM job j WHERE j.id=sqlc.arg(job_id)::uuid AND j.kind='automatic_tv_source' AND j.state='running'
 AND j.lease_owner=sqlc.arg(owner)::text AND j.lease_token=sqlc.arg(token)::bigint AND j.lease_until>clock_timestamp()
 AND j.dedupe_key=sqlc.arg(round_id)::uuid::text||':'||sqlc.arg(indexer_id)::uuid::text
 AND j.payload=jsonb_build_object('round_id',sqlc.arg(round_id)::uuid,'indexer_id',sqlc.arg(indexer_id)::uuid)
 AND clock_timestamp()<sqlc.arg(deadline)::timestamptz) AS valid;
-- name: AutomaticTVBlocked :one
SELECT (EXISTS(SELECT 1 FROM acquisition_reservation WHERE unit_id=sqlc.arg(unit_id)::uuid AND released_at IS NULL)
 OR EXISTS(SELECT 1 FROM unit_reservation WHERE unit_id=sqlc.arg(unit_id)::uuid AND state IN ('reserved','dispatched'))
 OR EXISTS(SELECT 1 FROM download_requirement WHERE unit_id=sqlc.arg(unit_id)::uuid AND state='pending')
 OR EXISTS(SELECT 1 FROM library_file_unit fu JOIN library_file f ON f.id=fu.file_id WHERE fu.unit_id=sqlc.arg(unit_id)::uuid AND fu.retired_at IS NULL AND f.retired_at IS NULL)
 OR EXISTS(SELECT 1 FROM library_reuse_unit WHERE unit_id=sqlc.arg(unit_id)::uuid AND active))::boolean AS blocked;
-- name: AutomaticTVLinkQuery :execrows
INSERT INTO automatic_tv_query(round_id,indexer_id,page,query_id,source_job_id,source_token,parameters,parameters_hash)
SELECT sqlc.arg(round_id)::uuid,sqlc.arg(indexer_id)::uuid,sqlc.arg(page)::integer,q.id,sqlc.arg(job_id)::uuid,sqlc.arg(token)::bigint,sqlc.arg(parameters)::jsonb,sqlc.arg(parameters_hash)::text
FROM indexer_query q WHERE q.id=sqlc.arg(query_id)::uuid AND q.indexer_id=sqlc.arg(indexer_id)::uuid AND q.page=sqlc.arg(page)::integer
 AND q.query_type='tv_episode' AND q.state='running' AND q.keyword='tv-episode-query-v1:'||sqlc.arg(parameters_hash)::text
 AND q.config_version=sqlc.arg(config_version)::bigint AND q.rss_job_id IS NULL
 AND sqlc.arg(page)::integer=(SELECT count(*) FROM automatic_tv_query a WHERE a.round_id=sqlc.arg(round_id)::uuid AND a.indexer_id=sqlc.arg(indexer_id)::uuid);
-- name: AutomaticTVSourceCurrent :one
SELECT EXISTS(SELECT 1 FROM indexer i JOIN indexer_configuration c ON c.indexer_id=i.id LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id
 WHERE i.id=$1 AND i.config_version=$2 AND i.health='online' AND c.protocol='torznab' AND c.configuration_version<=i.config_version
 AND COALESCE(l.enabled,true) AND l.deleted_at IS NULL) AS current;
-- name: AutomaticTVFreezeCandidate :exec
INSERT INTO automatic_tv_query_candidate(query_id,candidate_id,source_stamp,title,guid,info_hash,link_encrypted,quality_profile,size_bytes,source_config_version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);
-- name: AutomaticTVAired :one
SELECT air_date IS NOT NULL AND air_date<=CURRENT_DATE AS aired FROM media_unit WHERE id=$1;
