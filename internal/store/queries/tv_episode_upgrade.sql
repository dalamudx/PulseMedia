-- Dedicated single standard TV episode authority; ordinary paths may only use isolation guards.
-- Preserve physical session identity, lease checks, locks, and all runtime predicates.

-- name: ReadTVEpisodeUpgradeAuthority :one
SELECT actor_id,unit_id,old_file_id,candidate_id,version,snapshot,valid_until FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid AND subscription_id=sqlc.arg(subscription_id)::uuid FOR SHARE;

-- name: ReadTVEpisodeUpgradePhaseReceipt :one
SELECT request_hash,result FROM tv_episode_upgrade_phase WHERE actor_id=sqlc.arg(actor_id)::uuid AND request_key=sqlc.arg(request_key)::text;

-- name: InsertTVEpisodeUpgradePhase :exec
INSERT INTO tv_episode_upgrade_phase(operation_id,action,actor_id,request_key,request_hash,result,evidence) VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(action)::text,sqlc.arg(actor_id)::uuid,sqlc.arg(request_key)::text,sqlc.arg(request_hash)::text,sqlc.arg(result)::jsonb,sqlc.arg(evidence)::jsonb);

-- name: ReadTVEpisodeUpgradePhaseEvidence :one
SELECT evidence FROM tv_episode_upgrade_phase WHERE operation_id=sqlc.arg(operation_id)::uuid AND action=sqlc.arg(action)::text;

-- name: CheckTVEpisodeUpgradePermitExists :one
SELECT EXISTS(SELECT 1 FROM tv_episode_upgrade_permit WHERE operation_id=sqlc.arg(operation_id)::uuid AND action=sqlc.arg(action)::text);

-- name: InsertTVEpisodeUpgradePermit :exec
INSERT INTO tv_episode_upgrade_permit(operation_id,action,owner,token,backend_pid,backend_start,lock_key,nonce,lease_until,request_key,request_hash) VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(action)::text,sqlc.arg(owner)::uuid,sqlc.arg(token)::uuid,pg_backend_pid(),pulse_tv_upgrade_backend_start(),sqlc.arg(lock_key)::bigint,sqlc.arg(nonce)::uuid,clock_timestamp()+interval '60 seconds',sqlc.arg(request_key)::text,sqlc.arg(request_hash)::text);

-- name: CheckTVEpisodeUpgradePermit :one
SELECT EXISTS(SELECT 1 FROM tv_episode_upgrade_permit p WHERE p.operation_id=sqlc.arg(operation_id)::uuid AND p.action=sqlc.arg(action)::text AND p.owner=sqlc.arg(owner)::uuid AND p.token=sqlc.arg(token)::uuid AND p.backend_pid=pg_backend_pid() AND p.backend_start=pulse_tv_upgrade_backend_start() AND p.lock_key=sqlc.arg(lock_key)::bigint AND p.nonce=sqlc.arg(nonce)::uuid AND p.lease_until>clock_timestamp() AND p.request_key=sqlc.arg(request_key)::text AND p.request_hash=sqlc.arg(request_hash)::text);

-- name: InsertTVEpisodeUpgradePhysicalReturn :exec
INSERT INTO tv_episode_upgrade_return(operation_id,action,owner,token,status,evidence) VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(action)::text,sqlc.arg(owner)::uuid,sqlc.arg(token)::uuid,sqlc.arg(status)::text,sqlc.arg(evidence)::jsonb);

-- name: CheckTVEpisodeUpgradeObservationAllowed :one
SELECT EXISTS(SELECT 1 FROM tv_episode_upgrade_return WHERE operation_id=sqlc.arg(operation_id)::uuid AND action='send') AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_phase WHERE operation_id=sqlc.arg(operation_id)::uuid AND action='organize') AND NOT EXISTS(SELECT 1 FROM tv_episode_upgrade_permit p LEFT JOIN tv_episode_upgrade_return r USING(operation_id,action) WHERE p.operation_id=sqlc.arg(operation_id)::uuid AND p.action LIKE 'observe:%' AND (r.operation_id IS NULL OR r.returned_at>clock_timestamp()-interval '1 second'));

-- name: CheckTVEpisodeUpgradeAuthorityActor :one
SELECT EXISTS(SELECT 1 FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid AND actor_id=sqlc.arg(actor_id)::uuid AND subscription_id=sqlc.arg(subscription_id)::uuid);

-- name: InsertTVEpisodeUpgradeAuthority :exec
INSERT INTO tv_episode_upgrade_authority(operation_id,actor_id,subscription_id,unit_id,root_id,old_file_id,old_fact_id,candidate_id,client_id,version,snapshot,valid_until) VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(actor_id)::uuid,sqlc.arg(subscription_id)::uuid,sqlc.arg(unit_id)::uuid,sqlc.arg(root_id)::uuid,sqlc.arg(old_file_id)::uuid,sqlc.arg(old_fact_id)::uuid,sqlc.arg(candidate_id)::uuid,sqlc.arg(client_id)::uuid,sqlc.arg(version)::text,sqlc.arg(snapshot)::jsonb,sqlc.arg(valid_until)::timestamptz);

-- name: ReadTVEpisodeUpgradeCommandReceipt :one
SELECT p.result FROM tv_episode_upgrade_phase p JOIN tv_episode_upgrade_authority a USING(operation_id) WHERE p.actor_id=sqlc.arg(actor_id)::uuid AND p.request_key=sqlc.arg(request_key)::text AND a.actor_id=sqlc.arg(actor_id)::uuid AND a.subscription_id=sqlc.arg(subscription_id)::uuid;

-- name: ReadTVEpisodeUpgradeUnresolvedPermit :one
SELECT a.operation_id,a.unit_id,a.old_file_id,a.candidate_id,a.version FROM tv_episode_upgrade_permit p JOIN tv_episode_upgrade_authority a USING(operation_id) WHERE a.actor_id=sqlc.arg(actor_id)::uuid AND p.request_key=sqlc.arg(request_key)::text AND a.subscription_id=sqlc.arg(subscription_id)::uuid;

-- name: CheckTVEpisodeUpgradeSchemaInstalled :one
SELECT to_regclass('tv_episode_upgrade_authority') IS NOT NULL;

-- name: ReadTVEpisodeUpgradeLatestObservation :one
SELECT evidence FROM tv_episode_upgrade_phase WHERE operation_id=sqlc.arg(operation_id)::uuid AND action LIKE 'observe:%' ORDER BY created_at DESC,action DESC LIMIT 1;

-- name: CheckTVEpisodeUpgradeCompletedReturn :one
SELECT EXISTS(SELECT 1 FROM tv_episode_upgrade_return WHERE operation_id=sqlc.arg(operation_id)::uuid AND action='send' AND status='confirmed') OR EXISTS(SELECT 1 FROM tv_episode_upgrade_return WHERE operation_id=sqlc.arg(operation_id)::uuid AND action LIKE 'observe:%' AND status='confirmed' AND evidence=sqlc.arg(evidence)::jsonb);

-- name: RevokeTVEpisodeUpgradeOldFact :execrows
UPDATE unit_fact SET revoked_at=sqlc.arg(retired_at)::timestamptz,revocation_reason='quality_upgrade' WHERE id=(SELECT old_fact_id FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid) AND revoked_at IS NULL;

-- name: RetireTVEpisodeUpgradeOldMapping :execrows
UPDATE library_file_unit SET retired_at=sqlc.arg(retired_at)::timestamptz WHERE file_id=(SELECT old_file_id FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid) AND retired_at IS NULL;

-- name: RetireTVEpisodeUpgradeOldFile :execrows
UPDATE library_file f SET retired_at=sqlc.arg(retired_at)::timestamptz,last_mutation_seq=r.mutation_seq+1 FROM tv_episode_upgrade_authority a JOIN library_root r ON r.id=a.root_id WHERE a.operation_id=sqlc.arg(operation_id)::uuid AND f.id=a.old_file_id AND f.retired_at IS NULL;

-- name: AdvanceTVEpisodeUpgradeRetiredRoot :execrows
UPDATE library_root SET mutation_seq=mutation_seq+1,updated_at=sqlc.arg(retired_at)::timestamptz WHERE id=(SELECT root_id FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid);

-- name: CheckTVEpisodeUpgradeRetirementBasis :one
SELECT EXISTS(SELECT 1 FROM tv_episode_upgrade_return WHERE operation_id=sqlc.arg(operation_id)::uuid AND action='organize' AND status='confirmed' AND evidence=sqlc.arg(evidence)::jsonb) AND EXISTS(SELECT 1 FROM library_file f JOIN library_file_unit m ON m.file_id=f.id JOIN unit_fact fact ON fact.source_file_id=f.id JOIN tv_episode_upgrade_authority a ON a.old_file_id=f.id WHERE a.operation_id=sqlc.arg(operation_id)::uuid AND f.retired_at IS NULL AND m.retired_at IS NULL AND m.unit_id=a.unit_id AND fact.id=a.old_fact_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp()) AND (SELECT count(*) FROM library_file_unit WHERE file_id=f.id)=1 AND (SELECT count(*) FROM unit_fact WHERE source_file_id=f.id AND revoked_at IS NULL)=1);

-- name: ReadTVEpisodeUpgradeRetirementQuality :one
SELECT f.quality_profile,r.rules FROM tv_episode_upgrade_authority a JOIN unit_fact f ON f.id=a.old_fact_id JOIN subscription s ON s.id=a.subscription_id JOIN policy p ON p.id=s.quality_policy_id JOIN policy_revision r ON r.policy_id=p.id AND r.revision=p.current_revision WHERE a.operation_id=sqlc.arg(operation_id)::uuid AND s.state='active' AND s.quality_policy_revision=p.current_revision;

-- name: ReadTVEpisodeUpgradeOldFactID :one
SELECT old_fact_id FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid;

-- name: CheckTVEpisodeUpgradeDedicatedDemand :one
SELECT EXISTS(SELECT 1 FROM download_requirement WHERE operation_id=sqlc.arg(operation_id)::uuid AND (submission_fence->>'goal'='tv_episode_upgrade' OR submission_fence->>'kind'='tv_episode_upgrade_v1'));

-- name: CheckTVEpisodeUpgradeOperationIsolated :one
SELECT EXISTS(SELECT 1 FROM download_requirement WHERE operation_id=sqlc.arg(operation_id)::uuid AND (submission_fence->>'goal'='tv_episode_upgrade' OR submission_fence->>'kind'='tv_episode_upgrade_v1')) OR EXISTS(SELECT 1 FROM tv_episode_upgrade_authority WHERE operation_id=sqlc.arg(operation_id)::uuid);


-- name: ReadTVEpisodeUpgradeEvidence :one
-- Scan progress is not authority; identity, generation, health and actual facts remain frozen.
SELECT jsonb_build_object(
 'actor',(SELECT to_jsonb(x)-'password_hash' FROM app_user x WHERE id=sqlc.arg(actor_id)::uuid),
 'subscription',(SELECT to_jsonb(x) FROM subscription x WHERE id=sqlc.arg(subscription_id)::uuid),
 'policy',(SELECT to_jsonb(x) FROM policy x WHERE id=sqlc.arg(policy_id)::text),
 'revision',(SELECT to_jsonb(x) FROM policy_revision x WHERE policy_id=sqlc.arg(policy_id)::text AND revision=sqlc.arg(policy_revision)::bigint),
 'root',(SELECT to_jsonb(x)-ARRAY['scan_token','mutation_seq','updated_at']::text[] FROM library_root x WHERE id=sqlc.arg(root_id)::uuid),
 'root_identity',(SELECT jsonb_build_object('device',sf.root_device,'inode',sf.root_inode) FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id WHERE sf.root_id=sqlc.arg(root_id)::uuid AND sr.generation=(SELECT generation FROM library_root WHERE id=sqlc.arg(root_id)::uuid) AND sr.state='succeeded' AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1),
 'media',(SELECT to_jsonb(x) FROM media x WHERE id=sqlc.arg(media_id)::uuid),
 'unit',(SELECT to_jsonb(x)-ARRAY['facts_version','updated_at']::text[] FROM media_unit x WHERE id=sqlc.arg(unit_id)::uuid),
 'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM media_identity x WHERE media_id=sqlc.arg(media_id)::uuid),
 'unit_identity',(SELECT to_jsonb(x) FROM media_unit_identity x WHERE unit_id=sqlc.arg(unit_id)::uuid AND namespace='tmdb.episode'),
 'episode_mapping',(SELECT to_jsonb(x) FROM episode_mapping_revision x WHERE subscription_id=sqlc.arg(subscription_id)::uuid AND revision=(SELECT episode_mapping_revision FROM subscription WHERE id=sqlc.arg(subscription_id)::uuid)),
 'files',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM library_file x WHERE id=sqlc.arg(file_id)::uuid OR id IN (SELECT file_id FROM library_file_unit WHERE unit_id=sqlc.arg(unit_id)::uuid)),
 'mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_file_unit x WHERE unit_id=sqlc.arg(unit_id)::uuid OR file_id=sqlc.arg(file_id)::uuid),
 'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE unit_id=sqlc.arg(unit_id)::uuid OR source_file_id=sqlc.arg(file_id)::uuid),
 'wanted',(SELECT jsonb_agg(jsonb_build_object('id',id,'unit_id',unit_id,'intent_version',intent_version,'user_skipped',user_skipped,'scope_excluded',scope_excluded) ORDER BY id) FROM wanted_item WHERE subscription_id=sqlc.arg(subscription_id)::uuid AND unit_id=sqlc.arg(unit_id)::uuid),
 'blocks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM release_block x WHERE subscription_id=sqlc.arg(subscription_id)::uuid),
 'candidate',(SELECT to_jsonb(x) FROM release_candidate x WHERE id=sqlc.arg(candidate_id)::uuid),
 'indexer',(SELECT to_jsonb(x) FROM indexer x WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=sqlc.arg(candidate_id)::uuid)),
 'source_configuration',(SELECT to_jsonb(x) FROM indexer_configuration x WHERE indexer_id=(SELECT indexer_id FROM release_candidate WHERE id=sqlc.arg(candidate_id)::uuid)),
 'queries',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM indexer_query x WHERE id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=sqlc.arg(candidate_id)::uuid)),
 'query_candidates',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM indexer_query_candidate x WHERE candidate_id=sqlc.arg(candidate_id)::uuid),
 'client',(SELECT to_jsonb(x) FROM download_client x WHERE id=(SELECT downloader_id FROM subscription WHERE id=sqlc.arg(subscription_id)::uuid)),
 'reuse',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_reuse_unit x WHERE unit_id=sqlc.arg(unit_id)::uuid)
)::jsonb AS body;


-- name: ReadTVEpisodeUpgradeOrganizationState :one
SELECT (snapshot || jsonb_build_object(
 'lanes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE unit_id=sqlc.arg(unit_id)::uuid),
 'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE unit_id=sqlc.arg(unit_id)::uuid),
 'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE unit_id=sqlc.arg(unit_id)::uuid)
))::jsonb AS body FROM (
SELECT jsonb_build_object(
 'actor',(SELECT to_jsonb(x)-'password_hash' FROM app_user x WHERE id=sqlc.arg(actor_id)::uuid),
 'subscription',(SELECT to_jsonb(x) FROM subscription x WHERE id=sqlc.arg(subscription_id)::uuid),
 'policy',(SELECT to_jsonb(x) FROM policy x WHERE id=sqlc.arg(policy_id)::text),
 'revision',(SELECT to_jsonb(x) FROM policy_revision x WHERE policy_id=sqlc.arg(policy_id)::text AND revision=sqlc.arg(policy_revision)::bigint),
 'root',(SELECT to_jsonb(x)-'scan_token' FROM library_root x WHERE id=sqlc.arg(root_id)::uuid),
 'root_identity',(SELECT jsonb_build_object('device',sf.root_device,'inode',sf.root_inode) FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id WHERE sf.root_id=sqlc.arg(root_id)::uuid AND sr.generation=(SELECT generation FROM library_root WHERE id=sqlc.arg(root_id)::uuid) AND sr.state='succeeded' AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1),
 'media',(SELECT to_jsonb(x) FROM media x WHERE id=sqlc.arg(media_id)::uuid),
 'unit',(SELECT to_jsonb(x)-ARRAY['facts_version','updated_at']::text[] FROM media_unit x WHERE id=sqlc.arg(unit_id)::uuid),
 'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM media_identity x WHERE media_id=sqlc.arg(media_id)::uuid),
 'unit_identity',(SELECT to_jsonb(x) FROM media_unit_identity x WHERE unit_id=sqlc.arg(unit_id)::uuid AND namespace='tmdb.episode'),
 'episode_mapping',(SELECT to_jsonb(x) FROM episode_mapping_revision x WHERE subscription_id=sqlc.arg(subscription_id)::uuid AND revision=(SELECT episode_mapping_revision FROM subscription WHERE id=sqlc.arg(subscription_id)::uuid)),
 'files',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM library_file x WHERE id=sqlc.arg(file_id)::uuid OR id IN (SELECT file_id FROM library_file_unit WHERE unit_id=sqlc.arg(unit_id)::uuid)),
 'mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_file_unit x WHERE unit_id=sqlc.arg(unit_id)::uuid OR file_id=sqlc.arg(file_id)::uuid),
 'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE unit_id=sqlc.arg(unit_id)::uuid OR source_file_id=sqlc.arg(file_id)::uuid),
 'wanted',(SELECT jsonb_agg(jsonb_build_object('id',id,'unit_id',unit_id,'intent_version',intent_version,'user_skipped',user_skipped,'scope_excluded',scope_excluded) ORDER BY id) FROM wanted_item WHERE subscription_id=sqlc.arg(subscription_id)::uuid AND unit_id=sqlc.arg(unit_id)::uuid),
 'blocks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM release_block x WHERE subscription_id=sqlc.arg(subscription_id)::uuid),
 'candidate',(SELECT to_jsonb(x) FROM release_candidate x WHERE id=sqlc.arg(candidate_id)::uuid),
 'indexer',(SELECT to_jsonb(x) FROM indexer x WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=sqlc.arg(candidate_id)::uuid)),
 'source_configuration',(SELECT to_jsonb(x) FROM indexer_configuration x WHERE indexer_id=(SELECT indexer_id FROM release_candidate WHERE id=sqlc.arg(candidate_id)::uuid)),
 'queries',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM indexer_query x WHERE id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=sqlc.arg(candidate_id)::uuid)),
 'query_candidates',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM indexer_query_candidate x WHERE candidate_id=sqlc.arg(candidate_id)::uuid),
 'client',(SELECT to_jsonb(x) FROM download_client x WHERE id=(SELECT downloader_id FROM subscription WHERE id=sqlc.arg(subscription_id)::uuid)),
 'reuse',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_reuse_unit x WHERE unit_id=sqlc.arg(unit_id)::uuid)
)::jsonb AS body
) basis(snapshot);
