-- +goose Up
CREATE TABLE automatic_tv_organization_plan (
 plan_id UUID PRIMARY KEY REFERENCES transfer_plan(id),
 operation_id UUID NOT NULL UNIQUE REFERENCES automatic_tv_submission_preparation(operation_id),
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id),
 observation_job_id UUID NOT NULL REFERENCES automatic_tv_observation_result(job_id),
 organization_job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 step_id UUID NOT NULL UNIQUE REFERENCES transfer_step(id),
 authority_hash TEXT NOT NULL CHECK(authority_hash ~ '^[0-9a-f]{64}$'),
 proof_hash TEXT NOT NULL CHECK(proof_hash ~ '^[0-9a-f]{64}$'),
 inventory_stamp TEXT NOT NULL CHECK(inventory_stamp ~ '^[0-9a-f]{64}$'),
 source_root TEXT NOT NULL CHECK(left(source_root,1)='/'),
 source_rel TEXT NOT NULL CHECK(source_rel<>'' AND left(source_rel,1)<>'/'),
 source_directory TEXT NOT NULL CHECK(left(source_directory,1)='/'),
 source_root_device BIGINT NOT NULL,source_root_inode BIGINT NOT NULL,
 source_directory_device BIGINT NOT NULL,source_directory_inode BIGINT NOT NULL,
 source_device BIGINT NOT NULL,source_inode BIGINT NOT NULL,source_size BIGINT NOT NULL CHECK(source_size>0),source_mtime BIGINT NOT NULL,
 root_id UUID NOT NULL REFERENCES library_root(id),root_path TEXT NOT NULL,root_generation BIGINT NOT NULL,
 root_device BIGINT NOT NULL,root_inode BIGINT NOT NULL,baseline_device BIGINT NOT NULL,baseline_inode BIGINT NOT NULL,
 marker_filename TEXT NOT NULL,marker_device BIGINT NOT NULL,marker_inode BIGINT NOT NULL,marker_size BIGINT NOT NULL,marker_mtime BIGINT NOT NULL,
 quality_profile JSONB NOT NULL,target_evidence JSONB NOT NULL,client_snapshot JSONB NOT NULL,
 observed_at TIMESTAMPTZ NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(root_device=baseline_device AND root_inode=baseline_inode AND root_device=source_device)
);
CREATE TABLE automatic_tv_organization_observation (
 id UUID PRIMARY KEY,plan_id UUID NOT NULL REFERENCES automatic_tv_organization_plan(plan_id),
 step_id UUID NOT NULL REFERENCES transfer_step(id),execution_owner TEXT NOT NULL CHECK(execution_owner<>''),execution_token BIGINT NOT NULL CHECK(execution_token>0),
 job_id UUID NOT NULL REFERENCES job(id),lease_owner TEXT NOT NULL CHECK(lease_owner<>''),lease_token BIGINT NOT NULL CHECK(lease_token>0),
 observed_at TIMESTAMPTZ NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX automatic_tv_organization_observation_plan ON automatic_tv_organization_observation(plan_id,execution_token,lease_token,observed_at);
CREATE TABLE automatic_tv_organization_completion (
 plan_id UUID PRIMARY KEY REFERENCES automatic_tv_organization_plan(plan_id),step_id UUID NOT NULL UNIQUE REFERENCES transfer_step(id),
 file_id UUID NOT NULL UNIQUE REFERENCES library_file(id),job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),lease_token BIGINT NOT NULL CHECK(lease_token>0),
 lease_until TIMESTAMPTZ NOT NULL,plan_lease_until TIMESTAMPTZ NOT NULL,
 observation_id UUID NOT NULL UNIQUE REFERENCES automatic_tv_organization_observation(id),
 result JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER automatic_tv_organization_plan_history BEFORE UPDATE OR DELETE ON automatic_tv_organization_plan FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_organization_plan_truncate BEFORE TRUNCATE ON automatic_tv_organization_plan FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_organization_observation_history BEFORE UPDATE OR DELETE ON automatic_tv_organization_observation FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_organization_observation_truncate BEFORE TRUNCATE ON automatic_tv_organization_observation FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_organization_completion_history BEFORE UPDATE OR DELETE ON automatic_tv_organization_completion FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_submission_history();
CREATE TRIGGER automatic_tv_organization_completion_truncate BEFORE TRUNCATE ON automatic_tv_organization_completion FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_submission_history();

-- +goose StatementBegin
CREATE FUNCTION automatic_tv_organization_bound(p automatic_tv_organization_plan) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT COALESCE(EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
 JOIN automatic_tv_submission_preparation prep ON prep.operation_id=a.operation_id
 JOIN automatic_tv_observation_result inv ON inv.job_id=p.observation_job_id AND inv.operation_id=a.operation_id AND inv.task_id=prep.task_id AND inv.complete AND NOT inv.review_required
 JOIN automatic_tv_observation_job observed ON observed.job_id=inv.job_id AND observed.operation_id=a.operation_id AND observed.task_id=prep.task_id AND observed.send_attempt_id=prep.physical_attempt_id
 JOIN automatic_tv_physical_attempt physical ON physical.id=prep.physical_attempt_id AND physical.finished_at IS NOT NULL
 JOIN job receipt ON receipt.id=inv.job_id AND receipt.state='succeeded' AND receipt.lease_token=inv.lease_token
 JOIN job j ON j.id=p.organization_job_id AND j.kind='download_organization' AND j.dedupe_key='organize:'||p.task_id::text
 JOIN transfer_plan plan ON plan.id=p.plan_id AND plan.operation_id=a.operation_id AND plan.root_id=a.root_id
 JOIN transfer_step step ON step.id=p.step_id AND step.plan_id=plan.id AND step.unit_id=a.unit_id
 JOIN download_task task ON task.id=p.task_id AND task.operation_id=a.operation_id
 JOIN release_candidate candidate ON candidate.id=a.candidate_id
 JOIN download_client client ON client.id=a.client_id
 WHERE a.operation_id=p.operation_id AND a.snapshot->>'kind'='automatic_standard_single_episode_v1'
 AND prep.task_id=p.task_id AND p.authority_hash=prep.authority_hash AND p.proof_hash=prep.proof_hash
 AND inv.authority_hash=p.authority_hash AND inv.proof_hash=p.proof_hash AND inv.evidence_stamp=p.inventory_stamp
 AND j.payload=jsonb_build_object('operation_id',a.operation_id,'task_id',p.task_id,'automatic_tv_authority',a.operation_id,'observation_job_id',inv.job_id,'inventory_stamp',inv.evidence_stamp)
 AND task.state='completed' AND task.transfer_plan_id=plan.id AND task.client_id=a.client_id AND task.candidate_id=a.candidate_id
 AND task.info_hash=prep.info_hash AND task.save_path=prep.proof->>'save_path' AND task.total_bytes=(prep.proof->>'total_bytes')::bigint AND task.progress=1 AND task.downloaded_bytes=task.total_bytes
 AND plan.strategy='hardlink' AND NOT plan.requires_approval AND plan.old_file_id IS NULL
 AND plan.root_id=p.root_id AND plan.root_path=p.root_path AND plan.root_generation=p.root_generation AND plan.root_device=p.root_device AND plan.root_inode=p.root_inode
 AND step.action='hardlink' AND step.step_order=1 AND step.source_root=p.source_root AND step.source_rel=p.source_rel
 AND step.source_path=p.source_root||'/'||p.source_rel AND step.target_path=p.root_path||'/'||step.target_rel
 AND (EXISTS(SELECT 1 FROM library_root configured WHERE configured.path=p.source_root AND configured.health='online') OR EXISTS(SELECT 1 FROM jsonb_each_text(client.remote_path_map) mapping WHERE mapping.value=p.source_root))
 AND step.source_path=COALESCE((SELECT rtrim(mapping.value,'/')||substring(rtrim(prep.proof->>'save_path','/')||'/'||(prep.proof->'video'->>'Path') FROM length(rtrim(mapping.key,'/'))+1) FROM jsonb_each_text(client.remote_path_map) mapping WHERE rtrim(prep.proof->>'save_path','/')||'/'||(prep.proof->'video'->>'Path')=rtrim(mapping.key,'/') OR starts_with(rtrim(prep.proof->>'save_path','/')||'/'||(prep.proof->'video'->>'Path'),rtrim(mapping.key,'/')||'/') ORDER BY length(mapping.key) DESC,mapping.key LIMIT 1),rtrim(prep.proof->>'save_path','/')||'/'||(prep.proof->'video'->>'Path'))
 AND step.source_device=p.source_device AND step.source_inode=p.source_inode AND step.source_size=p.source_size AND step.source_mtime=p.source_mtime
 AND p.source_size=(prep.proof->'video'->>'Size')::bigint AND p.source_directory=regexp_replace(step.source_path,'/[^/]+$','')
 AND step.quality_profile=p.quality_profile AND candidate.quality_profile=p.quality_profile AND step.target_evidence=p.target_evidence
 AND p.target_evidence->>'automatic_tv_operation_id'=a.operation_id::text AND p.target_evidence->>'automatic_tv_inventory_stamp'=p.inventory_stamp
 AND NOT p.target_evidence ?| ARRAY['episode_operation_id','complete_tv_plan_id','file_action']
 AND p.target_evidence->>'inventory_task_id'=task.id::text AND p.target_evidence->>'inventory_source_path'=step.source_path
 AND (p.target_evidence->>'inventory_size_bytes')::bigint=p.source_size
 AND p.target_evidence->>'unit_id'=a.unit_id::text AND p.target_evidence->>'unit_key'=a.snapshot->'target'->>'unit_key'
 AND (p.target_evidence->>'unit_version')::bigint=(a.snapshot->'target'->>'unit_version')::bigint
 AND p.target_evidence->>'media_id'=a.snapshot->'target'->>'media_id' AND (p.target_evidence->>'media_version')::bigint=(a.snapshot->'target'->>'media_version')::bigint
 AND p.target_evidence->>'parser_version'=a.snapshot->'target'->>'parser_version'
 AND (SELECT count(*) FROM transfer_step s WHERE s.plan_id=plan.id)=1
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt x WHERE x.operation_id=a.operation_id AND x.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_permit x WHERE x.operation_id=a.operation_id AND x.finished_at IS NULL)),false);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION automatic_tv_organization_current(p automatic_tv_organization_plan) RETURNS boolean LANGUAGE sql VOLATILE AS $$
 SELECT COALESCE(EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
 JOIN app_user owner ON owner.id=a.owner_id AND owner.is_active AND owner.role IN ('member','admin')
 JOIN subscription sub ON sub.id=a.subscription_id AND sub.user_id=owner.id AND sub.state='active' AND sub.upgrade_mode='disabled'
 JOIN policy pol ON pol.id=sub.quality_policy_id AND pol.current_revision=sub.quality_policy_revision
 JOIN policy_revision revision ON revision.policy_id=pol.id AND revision.revision=pol.current_revision
 JOIN wanted_item w ON w.id=(a.snapshot->'target'->>'wanted_id')::uuid AND w.subscription_id=sub.id AND w.unit_id=a.unit_id
 JOIN media_unit unit ON unit.id=a.unit_id AND unit.media_id=sub.media_id
 JOIN media m ON m.id=unit.media_id AND m.media_type='tv'
 JOIN library_root root ON root.id=a.root_id AND root.media_type='tv' AND root.health='online' AND root.baseline_completed
 JOIN media_unit_identity ep ON ep.unit_id=unit.id AND ep.namespace='tmdb.episode'
 JOIN indexer idx ON idx.id=a.indexer_id AND idx.health='online'
 JOIN indexer_configuration conf ON conf.indexer_id=idx.id AND conf.protocol='torznab' AND conf.configuration_version<=idx.config_version
 JOIN indexer_lifecycle lifecycle ON lifecycle.indexer_id=idx.id AND lifecycle.enabled AND lifecycle.deleted_at IS NULL
 JOIN download_client client ON client.id=a.client_id AND to_jsonb(client)=p.client_snapshot
 JOIN release_candidate c ON c.id=a.candidate_id AND c.indexer_id=idx.id AND c.source_config_version=idx.config_version
 JOIN automatic_tv_query_candidate original ON original.query_id=a.query_id AND original.candidate_id=c.id
 WHERE a.operation_id=p.operation_id AND a.snapshot->'target'->>'mapping_mode'='standard'
 AND sub.media_id=(a.snapshot->'target'->>'media_id')::uuid AND sub.root_id=a.root_id AND sub.downloader_id=a.client_id
 AND sub.intent_version=(a.snapshot->'target'->>'intent_version')::bigint AND w.intent_version=sub.intent_version
 AND sub.quality_policy_id=a.snapshot->'target'->>'policy_id' AND sub.quality_policy_revision=(a.snapshot->'target'->>'policy_revision')::bigint AND revision.rules=a.snapshot->'target'->'rules'
 AND COALESCE(sub.episode_mapping_revision,0)=(a.snapshot->'target'->>'mapping_revision')::bigint
 AND (sub.episode_mapping_revision IS NULL OR EXISTS(SELECT 1 FROM episode_mapping_revision mapping WHERE mapping.subscription_id=sub.id AND mapping.revision=sub.episode_mapping_revision AND mapping.mode='standard' AND mapping.media_id=m.id AND mapping.media_version=m.metadata_version))
 AND NOT w.is_skipped AND NOT w.user_skipped AND NOT w.scope_excluded AND NOT unit.retired AND unit.absolute_number IS NULL
 AND unit.unit_key=a.snapshot->'target'->>'unit_key' AND unit.metadata_version=(a.snapshot->'target'->>'unit_version')::bigint
 AND unit.season_number=(a.snapshot->'target'->>'season')::integer AND unit.episode_number=(a.snapshot->'target'->>'episode')::integer
 AND unit.air_date IS NOT NULL AND unit.air_date<=CURRENT_DATE AND unit.air_date=(a.snapshot->'target'->>'air_date')::timestamptz::date
 AND m.metadata_version=(a.snapshot->'target'->>'media_version')::bigint AND m.title=a.snapshot->'target'->>'title'
 AND COALESCE(m.original_title,'')=a.snapshot->'target'->>'original_title' AND m.year=(a.snapshot->'target'->>'year')::integer
 AND ep.external_id=(a.snapshot->'target'->>'episode_id')::bigint AND ep.tmdb_series_id=(a.snapshot->'target'->>'tmdb_id')::bigint
 AND EXISTS(SELECT 1 FROM media_identity mi WHERE mi.media_id=m.id AND mi.namespace='tmdb.tv' AND mi.external_id=a.snapshot->'target'->>'tmdb_id')
 AND COALESCE(NULLIF(root.marker_filename,''),'.pulsemedia_root')=p.marker_filename
 AND root.path=p.root_path AND root.generation=p.root_generation AND root.generation=(a.snapshot->'target'->>'root_generation')::bigint
 AND root.health_revision=(a.snapshot->'target'->>'root_health_revision')::bigint AND encode(sha256(convert_to(root.path,'UTF8')),'hex')=a.snapshot->>'root_path_digest'
 AND (SELECT ROW(f.root_device,f.root_inode) FROM scan_finalization f JOIN scan_run scan ON scan.id=f.scan_run_id WHERE f.root_id=root.id AND scan.generation=root.generation AND scan.state='succeeded' AND f.completed_at IS NOT NULL ORDER BY scan.created_at,scan.id LIMIT 1)=ROW(p.baseline_device,p.baseline_inode)
 AND idx.config_version=(a.snapshot->>'source_config_version')::bigint AND sub.indexer_ids ? idx.id::text
 AND original.title=c.title AND original.guid=c.guid AND original.info_hash IS NOT DISTINCT FROM c.info_hash
 AND original.link_encrypted=c.link_encrypted AND original.quality_profile=c.quality_profile AND original.size_bytes=c.size_bytes AND original.source_config_version=c.source_config_version),false);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION automatic_tv_organization_completed(plan UUID) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT COALESCE(EXISTS(SELECT 1 FROM automatic_tv_organization_completion c
 JOIN automatic_tv_organization_plan p ON p.plan_id=c.plan_id
 JOIN automatic_tv_execution_authority a ON a.operation_id=p.operation_id
 JOIN automatic_tv_organization_observation o ON o.id=c.observation_id AND o.plan_id=p.plan_id AND o.step_id=p.step_id AND o.job_id=p.organization_job_id AND o.lease_owner=c.lease_owner AND o.lease_token=c.lease_token
 JOIN transfer_plan tp ON tp.id=p.plan_id AND tp.state='completed' AND tp.execution_owner=o.execution_owner AND tp.execution_token=o.execution_token
 JOIN transfer_step step ON step.id=p.step_id AND step.state='completed' AND step.target_file_id=c.file_id
 JOIN download_task task ON task.id=p.task_id AND task.state='completed' AND task.organization_state='completed' AND task.transfer_plan_id=p.plan_id
 JOIN job j ON j.id=p.organization_job_id AND j.state='succeeded' AND j.lease_token=c.lease_token AND j.lease_owner IS NULL AND j.lease_until IS NULL AND j.result=c.result
 JOIN library_file file ON file.id=c.file_id AND file.root_id=p.root_id AND file.rel_path=step.target_rel AND file.size_bytes=p.source_size AND file.mtime_ns=p.source_mtime AND file.retired_at IS NULL
 WHERE p.plan_id=plan AND c.step_id=p.step_id AND c.job_id=p.organization_job_id AND automatic_tv_organization_bound(p)
 AND file.file_fingerprint='stat.v1:'||p.source_inode::text||':'||p.source_size::text||':'||p.source_mtime::text
 AND c.result=jsonb_build_object('task_id',p.task_id,'plan_id',p.plan_id,'state','completed','automatic_tv_inventory_stamp',p.inventory_stamp)
 AND (SELECT count(*) FROM library_file_unit u WHERE u.file_id=file.id AND u.retired_at IS NULL)=1
 AND EXISTS(SELECT 1 FROM library_file_unit u JOIN transfer_step s ON s.id=p.step_id WHERE u.file_id=file.id AND u.unit_id=s.unit_id AND u.retired_at IS NULL)
 AND (SELECT count(*) FROM unit_fact f JOIN transfer_step s ON s.id=p.step_id WHERE f.unit_id=s.unit_id AND f.source_file_id=file.id AND f.root_id=p.root_id AND f.stage='imported' AND f.revoked_at IS NULL AND f.quality_profile=p.quality_profile)=1
 AND (SELECT count(*) FROM library_file_unit fu JOIN library_file other ON other.id=fu.file_id WHERE fu.unit_id=step.unit_id AND fu.retired_at IS NULL AND other.retired_at IS NULL)=1
 AND NOT EXISTS(SELECT 1 FROM unit_fact f WHERE f.unit_id=step.unit_id AND f.revoked_at IS NULL AND f.stage IN ('downloaded','imported') AND (f.source_file_id IS DISTINCT FROM file.id OR f.root_id IS DISTINCT FROM p.root_id OR f.quality_profile IS DISTINCT FROM p.quality_profile OR f.expires_at IS NOT NULL AND f.expires_at<=clock_timestamp()))
 AND (SELECT count(*) FROM outbox_event event WHERE event.event_type='media.imported' AND event.payload->>'operation_id'=p.operation_id::text)=1
 AND EXISTS(SELECT 1 FROM outbox_event event WHERE event.event_type='media.imported' AND event.payload->>'operation_id'=p.operation_id::text AND event.payload->>'file_id'=file.id::text AND event.payload->>'unit_id'=step.unit_id::text AND event.payload->>'target_path'=step.target_path)
 AND (SELECT count(*) FROM audit_log audit WHERE audit.action='transfer.imported' AND audit.target_type='transfer_step' AND audit.target_id=p.step_id::text)=1
 AND NOT EXISTS(SELECT 1 FROM acquisition_reservation lane WHERE lane.unit_id=a.unit_id AND lane.operation_id<>p.operation_id AND lane.released_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM unit_reservation r WHERE r.unit_id=a.unit_id AND r.operation_id<>p.operation_id AND r.state IN ('reserved','dispatched'))
 AND NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.unit_id=a.unit_id AND d.operation_id<>p.operation_id AND d.state='pending')
 AND (SELECT count(*) FROM download_requirement d WHERE d.operation_id=p.operation_id)=1
 AND EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=p.operation_id AND d.state='imported' AND d.unit_id=a.unit_id AND d.root_id=a.root_id AND d.actor_type='system' AND d.actor_id=a.round_id::text
 AND d.submission_fence=jsonb_build_object('policy_id',a.snapshot->'target'->'policy_id','policy_revision',a.snapshot->'target'->'policy_revision','subscription_id',a.subscription_id,'intent_version',a.snapshot->'target'->'intent_version','root_generation',a.snapshot->'target'->'root_generation','unit_metadata_version',a.snapshot->'target'->'unit_version','media_metadata_version',a.snapshot->'target'->'media_version','candidate_id',a.candidate_id,'root_id',a.root_id,'unit_id',a.unit_id,'media_id',a.snapshot->'target'->'media_id','actor_id',a.round_id::text,'actor_type','system','source_stamp',a.snapshot->'source_stamp','goal','acquire','kind','automatic_tv_episode_v1','authority',a.operation_id,'protected_digest',a.decision_stamp))
 AND NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=p.operation_id AND d.state<>'imported')
 AND (SELECT count(*) FROM unit_reservation r WHERE r.operation_id=p.operation_id)=1
 AND EXISTS(SELECT 1 FROM unit_reservation r WHERE r.operation_id=p.operation_id AND r.state='consumed' AND r.unit_id=a.unit_id AND r.root_id=a.root_id AND r.candidate_id=a.candidate_id AND r.purpose='acquire')
 AND NOT EXISTS(SELECT 1 FROM unit_reservation r WHERE r.operation_id=p.operation_id AND r.state<>'consumed')
 AND (SELECT count(*) FROM acquisition_reservation lane WHERE lane.operation_id=p.operation_id)=1
 AND EXISTS(SELECT 1 FROM acquisition_reservation lane WHERE lane.operation_id=p.operation_id AND lane.released_at IS NOT NULL AND lane.unit_id=a.unit_id AND lane.acquisition_lane='default' AND NOT lane.legacy_conflict AND lane.task_id=p.task_id AND lane.source_root_id=p.root_id AND lane.source_generation=p.root_generation AND lane.source_stamp=a.snapshot->>'source_stamp' AND lane.candidate_id=a.candidate_id)
 AND NOT EXISTS(SELECT 1 FROM acquisition_reservation lane WHERE lane.operation_id=p.operation_id AND lane.released_at IS NULL)),false);
$$;
-- +goose StatementEnd

-- The movie, reviewed-episode and complete-TV guard body remains unchanged.
-- Automatic evidence is handled by an independent trigger and deferred binding.
DROP TRIGGER transfer_target_evidence_guard ON transfer_step;
CREATE TRIGGER transfer_target_evidence_guard BEFORE UPDATE ON transfer_step FOR EACH ROW
 WHEN (NOT COALESCE(NEW.target_evidence ? 'automatic_tv_operation_id',false)) EXECUTE FUNCTION pulse_transfer_target_evidence_guard();

-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_organization_step() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op UUID; a automatic_tv_execution_authority; prep automatic_tv_submission_preparation;
BEGIN
 SELECT operation_id INTO op FROM transfer_plan WHERE id=COALESCE(NEW.plan_id,OLD.plan_id);
 SELECT * INTO a FROM automatic_tv_execution_authority WHERE operation_id=op;
 IF a.operation_id IS NULL AND NOT COALESCE(NEW.target_evidence ? 'automatic_tv_operation_id',false) THEN RETURN COALESCE(NEW,OLD); END IF;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'automatic TV step history is immutable'; END IF;
 IF TG_OP='UPDATE' AND OLD.target_evidence IS NOT NULL AND ROW(NEW.plan_id,NEW.unit_id,NEW.source_path,NEW.target_path,NEW.source_root,NEW.source_rel,NEW.target_rel,NEW.source_size,NEW.source_mtime,NEW.source_device,NEW.source_inode,NEW.quality_profile,NEW.target_evidence,NEW.action,NEW.step_order)
 IS DISTINCT FROM ROW(OLD.plan_id,OLD.unit_id,OLD.source_path,OLD.target_path,OLD.source_root,OLD.source_rel,OLD.target_rel,OLD.source_size,OLD.source_mtime,OLD.source_device,OLD.source_inode,OLD.quality_profile,OLD.target_evidence,OLD.action,OLD.step_order)
 THEN RAISE EXCEPTION 'automatic TV frozen step cannot change'; END IF;
 IF TG_OP='UPDATE' AND OLD.state='completed' AND ROW(NEW.state,NEW.target_file_id) IS DISTINCT FROM ROW(OLD.state,OLD.target_file_id) THEN RAISE EXCEPTION 'automatic TV completed step cannot change'; END IF;
 -- Creation precedes FreezeTransferStep in the same transaction.
 IF TG_OP='INSERT' AND NEW.target_evidence IS NULL THEN RETURN NEW; END IF;
 SELECT * INTO prep FROM automatic_tv_submission_preparation WHERE operation_id=op;
 IF a.operation_id IS NULL OR prep.operation_id IS NULL OR NOT COALESCE(
 NEW.action='hardlink' AND NEW.step_order=1 AND NEW.unit_id=a.unit_id
 AND NEW.target_evidence->>'automatic_tv_operation_id'=op::text
 AND NOT NEW.target_evidence ?| ARRAY['episode_operation_id','complete_tv_plan_id','file_action']
 AND NEW.target_evidence->>'inventory_task_id'=prep.task_id::text
 AND NEW.target_evidence->>'inventory_source_path'=NEW.source_path
 AND (NEW.target_evidence->>'inventory_size_bytes')::bigint=NEW.source_size
 AND NEW.source_size=(prep.proof->'video'->>'Size')::bigint
 AND NEW.target_evidence->>'unit_id'=a.unit_id::text
 AND NEW.target_evidence->>'unit_key'=a.snapshot->'target'->>'unit_key'
 AND (NEW.target_evidence->>'unit_version')::bigint=(a.snapshot->'target'->>'unit_version')::bigint
 AND NEW.target_evidence->>'media_id'=a.snapshot->'target'->>'media_id'
 AND (NEW.target_evidence->>'media_version')::bigint=(a.snapshot->'target'->>'media_version')::bigint
 AND NEW.target_evidence->>'parser_version'=a.snapshot->'target'->>'parser_version'
 AND EXISTS(SELECT 1 FROM automatic_tv_observation_result r WHERE r.operation_id=op AND r.task_id=prep.task_id AND r.complete AND NOT r.review_required AND r.evidence_stamp=NEW.target_evidence->>'automatic_tv_inventory_stamp'),false)
 THEN RAISE EXCEPTION 'automatic TV step requires exact original authority and inventory'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_organization_step_guard BEFORE INSERT OR UPDATE OR DELETE ON transfer_step FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_organization_step();

-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_organization_plan() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_organization_plan WHERE plan_id=OLD.id) THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'automatic TV plan history is immutable'; END IF;
  IF ROW(NEW.operation_id,NEW.root_id,NEW.root_path,NEW.root_generation,NEW.root_device,NEW.root_inode,NEW.old_file_id,NEW.strategy,NEW.requires_approval)
  IS DISTINCT FROM ROW(OLD.operation_id,OLD.root_id,OLD.root_path,OLD.root_generation,OLD.root_device,OLD.root_inode,OLD.old_file_id,OLD.strategy,OLD.requires_approval)
  OR OLD.state='completed' AND ROW(NEW.state,NEW.execution_owner,NEW.execution_token,NEW.finished_at) IS DISTINCT FROM ROW(OLD.state,OLD.execution_owner,OLD.execution_token,OLD.finished_at)
  THEN RAISE EXCEPTION 'automatic TV frozen plan cannot change'; END IF;
 END IF;
 RETURN COALESCE(NEW,OLD);
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_organization_plan_guard BEFORE UPDATE OR DELETE ON transfer_plan FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_organization_plan();

-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_organization_observation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p automatic_tv_organization_plan;
BEGIN
 SELECT * INTO p FROM automatic_tv_organization_plan WHERE plan_id=NEW.plan_id;
 IF p.plan_id IS NULL OR NEW.step_id<>p.step_id OR NEW.job_id<>p.organization_job_id
 OR NOT EXISTS(SELECT 1 FROM transfer_plan tp WHERE tp.id=p.plan_id AND tp.state='executing' AND tp.execution_owner=NEW.execution_owner AND tp.execution_token=NEW.execution_token AND tp.lease_until>clock_timestamp())
 OR NOT EXISTS(SELECT 1 FROM job j WHERE j.id=p.organization_job_id AND j.state='running' AND j.lease_owner=NEW.lease_owner AND j.lease_token=NEW.lease_token AND j.lease_until>clock_timestamp())
 OR NEW.observed_at>clock_timestamp() OR NEW.observed_at<=clock_timestamp()-interval '5 seconds'
 THEN RAISE EXCEPTION 'automatic TV physical observation requires original live execution'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_organization_observation_binding BEFORE INSERT ON automatic_tv_organization_observation FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_observation();

-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_organization_publication() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p automatic_tv_organization_plan; c automatic_tv_organization_completion; o automatic_tv_organization_observation; chosen_plan UUID;
BEGIN
 IF TG_TABLE_NAME IN ('automatic_tv_organization_plan','automatic_tv_organization_completion') THEN chosen_plan:=NEW.plan_id;
 ELSIF TG_TABLE_NAME='transfer_plan' THEN
  IF NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=NEW.operation_id) THEN RETURN NULL; END IF;
  chosen_plan:=NEW.id;
 ELSIF TG_TABLE_NAME='transfer_step' THEN
  IF NOT EXISTS(SELECT 1 FROM transfer_plan tp JOIN automatic_tv_execution_authority a ON a.operation_id=tp.operation_id WHERE tp.id=NEW.plan_id) AND NOT COALESCE(NEW.target_evidence ? 'automatic_tv_operation_id',false) THEN RETURN NULL; END IF;
  chosen_plan:=NEW.plan_id;
 ELSIF TG_TABLE_NAME='download_task' THEN
  IF NEW.organization_state<>'completed' THEN RETURN NULL; END IF;
  IF NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=NEW.operation_id) THEN RETURN NULL; END IF;
  chosen_plan:=NEW.transfer_plan_id;
 ELSIF TG_TABLE_NAME='job' THEN
  IF NEW.kind<>'download_organization' OR NEW.state<>'succeeded' OR NOT NEW.payload ? 'automatic_tv_authority' THEN RETURN NULL; END IF;
  SELECT plan.plan_id INTO chosen_plan FROM automatic_tv_organization_plan plan WHERE organization_job_id=NEW.id;
 ELSIF TG_TABLE_NAME='unit_fact' THEN
  SELECT plan.plan_id INTO chosen_plan FROM automatic_tv_organization_plan plan JOIN transfer_step step ON step.id=plan.step_id
  WHERE step.unit_id=NEW.unit_id AND plan.root_id=NEW.root_id AND (EXISTS(SELECT 1 FROM acquisition_reservation lane WHERE lane.operation_id=plan.operation_id AND lane.released_at IS NULL) OR EXISTS(SELECT 1 FROM automatic_tv_organization_completion completed WHERE completed.plan_id=plan.plan_id AND completed.file_id=NEW.source_file_id));
  IF chosen_plan IS NULL THEN RETURN NULL; END IF;
 ELSIF TG_TABLE_NAME='outbox_event' THEN
  IF NEW.event_type<>'media.imported' OR NOT EXISTS(SELECT 1 FROM automatic_tv_execution_authority a WHERE a.operation_id::text=NEW.payload->>'operation_id') THEN RETURN NULL; END IF;
  SELECT plan.plan_id INTO chosen_plan FROM automatic_tv_organization_plan plan WHERE plan.operation_id::text=NEW.payload->>'operation_id';
 END IF;
 SELECT * INTO p FROM automatic_tv_organization_plan plan WHERE plan.plan_id=chosen_plan;
 IF p.plan_id IS NULL OR NOT automatic_tv_organization_bound(p) THEN RAISE EXCEPTION 'automatic TV publication requires unique original frozen plan'; END IF;
 IF TG_TABLE_NAME='automatic_tv_organization_plan' THEN
  IF p.observed_at>clock_timestamp() OR p.observed_at<=clock_timestamp()-interval '5 seconds' OR NOT automatic_tv_organization_current(p) THEN RAISE EXCEPTION 'automatic TV plan requires fresh eligible physical observation'; END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM transfer_plan tp WHERE tp.id=p.plan_id AND tp.state='completed')
 OR EXISTS(SELECT 1 FROM transfer_step step WHERE step.plan_id=p.plan_id AND step.state='completed')
 OR TG_TABLE_NAME IN ('automatic_tv_organization_completion','unit_fact','outbox_event')
 OR TG_TABLE_NAME IN ('download_task','job')
 THEN
  SELECT * INTO c FROM automatic_tv_organization_completion completion WHERE completion.plan_id=p.plan_id;
  SELECT * INTO o FROM automatic_tv_organization_observation observation WHERE observation.id=c.observation_id;
  IF c.plan_id IS NULL OR NOT automatic_tv_organization_completed(p.plan_id) OR NOT automatic_tv_organization_current(p)
  OR c.lease_until<=clock_timestamp() OR c.plan_lease_until<=clock_timestamp()
  OR o.observed_at>clock_timestamp() OR o.observed_at<=clock_timestamp()-interval '5 seconds'
  THEN RAISE EXCEPTION 'automatic TV facts, protections and terminal receipt must publish atomically under fresh leases'; END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER automatic_tv_organization_plan_binding AFTER INSERT ON automatic_tv_organization_plan DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_completion_binding AFTER INSERT ON automatic_tv_organization_completion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_plan_publication AFTER INSERT OR UPDATE ON transfer_plan DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_step_publication AFTER INSERT OR UPDATE ON transfer_step DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_task_publication AFTER UPDATE ON download_task DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_job_publication AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_fact_publication AFTER INSERT ON unit_fact DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.stage='imported') EXECUTE FUNCTION check_automatic_tv_organization_publication();
CREATE CONSTRAINT TRIGGER automatic_tv_organization_event_publication AFTER INSERT ON outbox_event DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.event_type='media.imported') EXECUTE FUNCTION check_automatic_tv_organization_publication();

-- +goose Down
LOCK TABLE automatic_tv_organization_completion,automatic_tv_organization_observation,automatic_tv_organization_plan,transfer_plan,transfer_step,download_task,job,unit_fact,outbox_event IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_organization_plan) OR EXISTS(SELECT 1 FROM automatic_tv_organization_observation) OR EXISTS(SELECT 1 FROM automatic_tv_organization_completion)
 OR EXISTS(SELECT 1 FROM transfer_step WHERE target_evidence ? 'automatic_tv_operation_id')
 THEN RAISE EXCEPTION 'automatic TV organization history downgrade refused'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER automatic_tv_organization_plan_publication ON transfer_plan;
DROP TRIGGER automatic_tv_organization_step_publication ON transfer_step;
DROP TRIGGER automatic_tv_organization_task_publication ON download_task;
DROP TRIGGER automatic_tv_organization_job_publication ON job;
DROP TRIGGER automatic_tv_organization_fact_publication ON unit_fact;
DROP TRIGGER automatic_tv_organization_event_publication ON outbox_event;
DROP TRIGGER automatic_tv_organization_step_guard ON transfer_step;
DROP TRIGGER automatic_tv_organization_plan_guard ON transfer_plan;
DROP TRIGGER transfer_target_evidence_guard ON transfer_step;
CREATE TRIGGER transfer_target_evidence_guard BEFORE UPDATE ON transfer_step FOR EACH ROW EXECUTE FUNCTION pulse_transfer_target_evidence_guard();
DROP TRIGGER automatic_tv_organization_plan_binding ON automatic_tv_organization_plan;
DROP TRIGGER automatic_tv_organization_completion_binding ON automatic_tv_organization_completion;
DROP TRIGGER automatic_tv_organization_observation_binding ON automatic_tv_organization_observation;
DROP FUNCTION check_automatic_tv_organization_publication();
DROP FUNCTION check_automatic_tv_organization_observation();
DROP FUNCTION guard_automatic_tv_organization_plan();
DROP FUNCTION guard_automatic_tv_organization_step();
DROP FUNCTION automatic_tv_organization_completed(UUID);
DROP FUNCTION automatic_tv_organization_current(automatic_tv_organization_plan);
DROP FUNCTION automatic_tv_organization_bound(automatic_tv_organization_plan);
DROP TABLE automatic_tv_organization_completion,automatic_tv_organization_observation,automatic_tv_organization_plan;
