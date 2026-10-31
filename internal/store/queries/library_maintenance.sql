-- SQL extracted from library maintenance runtime; statement order and transaction boundaries remain in Go.

-- name: LockLibraryMaintenanceUser :one
SELECT is_active,role FROM app_user WHERE id=sqlc.arg(actor_id)::uuid FOR SHARE;

-- name: GetLibraryMaintenanceSnapshot :one
SELECT pulse_library_maintenance_snapshot(sqlc.arg(file_id)::uuid)::jsonb AS snapshot;

-- name: LockLibraryMaintenanceRoot :exec
SELECT id FROM library_root WHERE id=sqlc.arg(root_id)::uuid FOR UPDATE;

-- name: LockLibraryMaintenanceUnits :exec
SELECT u.id FROM media_unit u JOIN library_file_unit fu ON fu.unit_id=u.id WHERE fu.file_id=sqlc.arg(file_id)::uuid ORDER BY u.id FOR UPDATE OF u;

-- name: LockLibraryMaintenanceMedia :exec
SELECT m.id FROM media m WHERE m.id IN(SELECT u.media_id FROM media_unit u JOIN library_file_unit fu ON fu.unit_id=u.id WHERE fu.file_id=sqlc.arg(file_id)::uuid) ORDER BY m.id FOR UPDATE;

-- name: LockLibraryMaintenanceFiles :exec
SELECT id FROM library_file WHERE id=sqlc.arg(file_id)::uuid OR id=sqlc.arg(other_id)::uuid ORDER BY id FOR UPDATE;

-- name: LockLibraryMaintenanceMappings :exec
SELECT unit_id FROM library_file_unit WHERE file_id=sqlc.arg(file_id)::uuid OR file_id=sqlc.arg(other_id)::uuid ORDER BY file_id,unit_id FOR UPDATE;

-- name: LockLibraryMaintenanceFacts :exec
SELECT id FROM unit_fact WHERE source_file_id=sqlc.arg(file_id)::uuid OR source_file_id=sqlc.arg(other_id)::uuid ORDER BY id FOR UPDATE;

-- name: GetLibraryVerifiedRecycle :one
SELECT to_jsonb(v) FROM library_verified_recycle v WHERE recycle_id=sqlc.arg(recycle_id)::text;

-- name: GetLibraryMaintenanceRecycleStatus :one
SELECT c.action,COALESCE(r.status,'unknown_requires_review')::text AS status FROM library_maintenance_command c LEFT JOIN library_maintenance_return r ON r.command_id=c.id WHERE c.recycle_id=sqlc.arg(recycle_id)::text AND c.id::text IS DISTINCT FROM current_setting('pulse.maintenance_current',true) ORDER BY c.created_at,c.id LIMIT 1;

-- name: GetLibraryMaintenanceClock :one
SELECT clock_timestamp()::timestamptz AS at;

-- name: LibraryMaintenanceTargetExists :one
SELECT EXISTS(SELECT 1 FROM library_file WHERE root_id=sqlc.arg(root_id)::uuid AND rel_path=sqlc.arg(target)::text AND retired_at IS NULL);

-- name: GetLibraryMaintenanceCommand :one
SELECT to_jsonb(c)::jsonb AS command,r.result FROM library_maintenance_command c LEFT JOIN library_maintenance_return r ON r.command_id=c.id WHERE c.actor_id=sqlc.arg(actor_id)::uuid AND c.request_key=sqlc.arg(request_key)::text;

-- name: SetLibraryMaintenanceNonce :exec
SELECT set_config('pulse.maintenance_nonce',sqlc.arg(nonce)::text,false);

-- name: CreateLibraryMaintenanceCommand :exec
INSERT INTO library_maintenance_command(id,actor_id,request_key,action,input,request_hash,root_id,file_id,recycle_id,frozen) VALUES(sqlc.arg(id)::uuid,sqlc.arg(actor_id)::uuid,sqlc.arg(request_key)::text,sqlc.arg(action)::text,sqlc.arg(input)::jsonb,sqlc.arg(request_hash)::text,sqlc.arg(root_id)::uuid,sqlc.arg(file_id)::uuid,NULLIF(sqlc.arg(recycle_id)::text,''),sqlc.arg(frozen)::jsonb);

-- name: CreateLibraryMaintenancePermit :exec
WITH n AS(SELECT clock_timestamp() t) INSERT INTO library_maintenance_permit(command_id,owner,token,backend_pid,backend_start,nonce,started_at,lease_until) SELECT sqlc.arg(command_id)::uuid,sqlc.arg(owner)::uuid,sqlc.arg(token)::uuid,pg_backend_pid(),(SELECT backend_start FROM pg_catalog.pg_stat_activity WHERE pid=pg_backend_pid()),sqlc.arg(nonce)::uuid,t,t+interval '60 seconds' FROM n;

-- name: CheckLibraryMaintenanceConstraints :exec
SET CONSTRAINTS ALL IMMEDIATE;

-- name: ValidateLibraryMaintenancePermit :one
SELECT (owner=sqlc.arg(owner)::uuid AND token=sqlc.arg(token)::uuid AND nonce=sqlc.arg(nonce)::uuid AND backend_pid=pg_backend_pid() AND backend_start=(SELECT backend_start FROM pg_catalog.pg_stat_activity WHERE pid=pg_backend_pid()) AND nonce::text=current_setting('pulse.maintenance_nonce',true) AND lease_until>clock_timestamp())::boolean AS valid FROM library_maintenance_permit WHERE command_id=sqlc.arg(command_id)::uuid;

-- name: SetLibraryMaintenanceCurrentCommand :exec
SELECT set_config('pulse.maintenance_current',sqlc.arg(command_id)::text,true);

-- name: ReorganizeLibraryMaintenanceFile :execrows
UPDATE library_file SET rel_path=sqlc.arg(target)::text,last_mutation_seq=(SELECT mutation_seq+1 FROM library_root WHERE id=sqlc.arg(root_id)::uuid) WHERE id=sqlc.arg(file_id)::uuid AND retired_at IS NULL;

-- name: RestoreLibraryMaintenanceFile :exec
INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint,last_mutation_seq,created_at) SELECT sqlc.arg(file_id)::uuid,sqlc.arg(root_id)::uuid,sqlc.arg(target)::text,sqlc.arg(size_bytes)::bigint,sqlc.arg(mtime_ns)::bigint,sqlc.arg(fingerprint)::text,mutation_seq+1,sqlc.arg(at)::timestamptz FROM library_root WHERE id=sqlc.arg(root_id)::uuid;

-- name: RestoreLibraryMaintenanceMapping :exec
INSERT INTO library_file_unit(file_id,unit_id,created_at) VALUES(sqlc.arg(file_id)::uuid,sqlc.arg(unit_id)::uuid,sqlc.arg(at)::timestamptz);

-- name: RestoreLibraryMaintenanceFact :exec
INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile,created_at) VALUES(sqlc.arg(fact_id)::uuid,sqlc.arg(unit_id)::uuid,'imported',sqlc.arg(file_id)::uuid,sqlc.arg(root_id)::uuid,sqlc.arg(profile)::jsonb,sqlc.arg(at)::timestamptz);

-- name: AdvanceLibraryMaintenanceRoot :exec
UPDATE library_root SET mutation_seq=mutation_seq+1,updated_at=sqlc.arg(at)::timestamptz WHERE id=sqlc.arg(root_id)::uuid;

-- name: GetLibraryMaintenanceUnknownAfterState :one
SELECT jsonb_build_object('source',pulse_library_maintenance_snapshot(sqlc.arg(file_id)::uuid),'replacement',CASE WHEN sqlc.narg(replacement_id)::uuid IS NOT NULL THEN pulse_library_maintenance_snapshot(sqlc.narg(replacement_id)::uuid) ELSE 'null'::jsonb END,'restored','null'::jsonb)::jsonb AS after_state;

-- name: CreateLibraryMaintenanceAudit :exec
INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload) VALUES(sqlc.arg(id)::uuid,'user',sqlc.arg(actor_id)::text,sqlc.arg(action)::text,'library_maintenance',sqlc.arg(target_id)::text,sqlc.arg(payload)::jsonb);

-- name: CreateLibraryMaintenanceEvent :exec
INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES(sqlc.arg(id)::uuid,'library_maintenance',sqlc.arg(aggregate_id)::text,sqlc.arg(event_type)::text,sqlc.arg(payload)::jsonb,1);

-- name: GetLibraryMaintenanceAfterState :one
SELECT jsonb_build_object('source',pulse_library_maintenance_snapshot(sqlc.arg(file_id)::uuid),'replacement',CASE WHEN sqlc.narg(replacement_id)::uuid IS NOT NULL THEN pulse_library_maintenance_snapshot(sqlc.narg(replacement_id)::uuid) ELSE 'null'::jsonb END,'restored',CASE WHEN sqlc.arg(restored)::boolean THEN pulse_library_maintenance_snapshot(sqlc.arg(restored_file_id)::uuid) ELSE 'null'::jsonb END)::jsonb AS after_state;

-- name: CreateLibraryMaintenanceReturn :exec
INSERT INTO library_maintenance_return(command_id,owner,token,status,result,after_state,audit_id,event_id) VALUES(sqlc.arg(command_id)::uuid,sqlc.arg(owner)::uuid,sqlc.arg(token)::uuid,sqlc.arg(status)::text,sqlc.arg(result)::jsonb,sqlc.arg(after_state)::jsonb,sqlc.arg(audit_id)::uuid,sqlc.arg(event_id)::uuid);

-- name: GetLibraryRecycleObservation :one
SELECT allocated_bytes,link_count,observed_at FROM library_recycle_observation WHERE recycle_id=sqlc.arg(recycle_id)::text AND identity=sqlc.arg(identity)::jsonb ORDER BY observed_at DESC,id DESC LIMIT 1;

-- name: ListLibraryVerifiedRecycle :many
SELECT to_jsonb(v) FROM library_verified_recycle v WHERE recycle_id>sqlc.arg(after_id)::text AND (sqlc.arg(kind)::text='' OR v.kind=sqlc.arg(kind)::text) AND (sqlc.arg(path_query)::text='' OR strpos(lower(v.original_rel),lower(sqlc.arg(path_query)::text))>0) ORDER BY recycle_id LIMIT sqlc.arg(page_limit)::int;

-- name: CreateLibraryRecycleObservation :exec
INSERT INTO library_recycle_observation(id,recycle_id,actor_id,identity,allocated_bytes,link_count) VALUES(sqlc.arg(id)::uuid,sqlc.arg(recycle_id)::text,sqlc.arg(actor_id)::uuid,sqlc.arg(identity)::jsonb,sqlc.arg(allocated_bytes)::bigint,sqlc.arg(link_count)::bigint);

-- name: GetLibraryMaintenanceExpectedAfterState :one
WITH input AS (
 SELECT sqlc.arg(source)::jsonb b,sqlc.narg(replacement)::jsonb rb,sqlc.arg(at)::timestamptz at,sqlc.arg(action)::text action,sqlc.arg(file_id)::uuid file_id,sqlc.arg(fact_id)::uuid fact_id,sqlc.narg(profile)::jsonb profile,sqlc.arg(target)::text target
 ), expected AS (
 SELECT b,rb,at,action,file_id,fact_id,profile,target,b->'root'||jsonb_build_object('mutation_seq',(b->'root'->>'mutation_seq')::bigint+1,'updated_at',at) root,
 (SELECT jsonb_agg(u||jsonb_build_object('facts_version',(u->>'facts_version')::bigint+CASE action WHEN 'reorganize' THEN 1 WHEN 'restore' THEN 2 ELSE 0 END) ORDER BY u->>'id') FROM jsonb_array_elements(b->'units') u) units
 FROM input
 ) SELECT jsonb_build_object(
 'source',b||jsonb_build_object('root',root,'units',units,'file',CASE WHEN action='reorganize' THEN b->'file'||jsonb_build_object('rel_path',target,'last_mutation_seq',(b->'root'->>'mutation_seq')::bigint+1) ELSE b->'file' END),
 'replacement',CASE WHEN rb IS NOT NULL THEN rb||jsonb_build_object('root',root,'units',units) ELSE 'null'::jsonb END,
 'restored',CASE WHEN action='restore' THEN b||jsonb_build_object('root',root,'units',units,
 'file',jsonb_build_object('id',file_id,'root_id',b->'root'->'id','rel_path',target,'size_bytes',b->'file'->'size_bytes','mtime_ns',b->'file'->'mtime_ns','file_fingerprint',b->'file'->'file_fingerprint','last_mutation_seq',(b->'root'->>'mutation_seq')::bigint+1,'retired_at',NULL,'created_at',at,'mapping_revision',2),
 'mappings',jsonb_build_array(jsonb_build_object('file_id',file_id,'unit_id',b->'units'->0->'id','created_at',at,'retired_at',NULL)),
 'facts',jsonb_build_array(jsonb_build_object('id',fact_id,'unit_id',b->'units'->0->'id','stage','imported','source_file_id',file_id,'source_task_id',NULL,'root_id',b->'root'->'id','quality_profile',profile,'expires_at',NULL,'revoked_at',NULL,'created_at',at,'revocation_reason',NULL))) ELSE 'null'::jsonb END)
::jsonb AS after_state FROM expected;
