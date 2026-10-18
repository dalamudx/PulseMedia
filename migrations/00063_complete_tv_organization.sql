-- +goose Up
-- Separate authority: no episode authority is fabricated and the old branches remain intact.
CREATE TABLE complete_tv_transfer_evidence (
 plan_id UUID PRIMARY KEY REFERENCES transfer_plan(id),
 task_id UUID NOT NULL UNIQUE REFERENCES download_task(id),
 operation_id UUID NOT NULL UNIQUE REFERENCES complete_tv_execution_authority(operation_id),
 tracking_job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 tracking_result JSONB NOT NULL CHECK(tracking_result->>'state'='completed' AND jsonb_array_length(tracking_result->'files') BETWEEN 2 AND 1000),
 baseline JSONB NOT NULL CHECK(jsonb_typeof(baseline)='object'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER complete_tv_transfer_evidence_immutable BEFORE UPDATE OR DELETE ON complete_tv_transfer_evidence FOR EACH ROW EXECUTE FUNCTION pulse_download_file_inventory_guard();

-- Normalize only the three fact revisions and one root watermark made by each
-- committed step. No historical authority, original version or plan is rewritten.
-- +goose StatementBegin
CREATE FUNCTION pulse_complete_tv_transfer_state(op UUID, plan UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
WITH own AS (SELECT unit_id,target_file_id FROM transfer_step WHERE plan_id=plan AND state='completed'),
units AS (SELECT unit_id FROM complete_tv_submission_unit WHERE task_id=(SELECT id FROM download_task WHERE operation_id=op))
SELECT jsonb_build_object(
 'units',(SELECT jsonb_agg(to_jsonb(u)||jsonb_build_object('facts_version',u.facts_version-CASE WHEN u.id IN(SELECT unit_id FROM own) THEN 3 ELSE 0 END-CASE WHEN EXISTS(SELECT 1 FROM acquisition_reservation lane JOIN transfer_plan p ON p.id=plan JOIN download_task t ON t.operation_id=op WHERE lane.operation_id=op AND lane.unit_id=u.id AND lane.released_at IS NOT NULL AND p.state='completed' AND t.organization_state='completed') THEN 1 ELSE 0 END) ORDER BY u.id) FROM media_unit u WHERE u.id IN(SELECT unit_id FROM units)),
 'root_seq',(SELECT r.mutation_seq-(SELECT count(*) FROM own) FROM library_root r JOIN transfer_plan p ON p.root_id=r.id WHERE p.id=plan),
 'requirements',(SELECT jsonb_agg((to_jsonb(d)-'state')||jsonb_build_object('state',CASE WHEN d.unit_id IN(SELECT unit_id FROM own) AND d.state='imported' THEN 'pending' ELSE d.state END) ORDER BY d.id) FROM download_requirement d WHERE operation_id=op),
 'reservations',(SELECT jsonb_agg((to_jsonb(r)-'updated_at'-'state')||jsonb_build_object('state',CASE WHEN r.unit_id IN(SELECT unit_id FROM own) AND r.state='consumed' THEN 'dispatched' ELSE r.state END) ORDER BY r.id) FROM unit_reservation r WHERE operation_id=op),
 'lanes',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM acquisition_reservation a WHERE operation_id=op),
 'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM unit_fact f WHERE unit_id IN(SELECT unit_id FROM units) AND NOT EXISTS(SELECT 1 FROM own WHERE own.target_file_id=f.source_file_id)),
 'files',(SELECT jsonb_agg(jsonb_build_array(to_jsonb(f),to_jsonb(fu)) ORDER BY f.id,fu.unit_id) FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id WHERE fu.unit_id IN(SELECT unit_id FROM units) AND f.id NOT IN(SELECT target_file_id FROM own)),
 'inventory',(SELECT to_jsonb(i) FROM download_file_inventory i WHERE operation_id=op),
 'prepared',(SELECT to_jsonb(p) FROM complete_tv_submission_preparation p WHERE operation_id=op),
 'authority',(SELECT to_jsonb(a) FROM complete_tv_execution_authority a WHERE operation_id=op),
 'task',(SELECT to_jsonb(t)-'updated_at'-'organization_state'-'organization_reason'-'transfer_plan_id' FROM download_task t WHERE operation_id=op)
);
$$;
-- +goose StatementEnd

-- This database branch validates the entire frozen set even when one step is
-- being frozen/completed. Fresh source/configuration checks also run in Go.
-- +goose StatementBegin
CREATE FUNCTION pulse_complete_tv_transfer_targets_valid(plan UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
SELECT COALESCE(EXISTS(
 SELECT 1 FROM complete_tv_transfer_evidence e JOIN transfer_plan p ON p.id=e.plan_id
 JOIN complete_tv_execution_authority a ON a.operation_id=e.operation_id
 JOIN complete_tv_submission_preparation prep ON prep.operation_id=e.operation_id
 JOIN download_task t ON t.id=e.task_id JOIN download_file_inventory inv ON inv.task_id=t.id
 JOIN job j ON j.id=e.tracking_job_id JOIN library_root r ON r.id=p.root_id
 JOIN subscription sub ON sub.id=a.subscription_id JOIN policy pol ON pol.id=sub.quality_policy_id
 WHERE e.plan_id=plan AND p.operation_id=e.operation_id AND prep.task_id=t.id AND t.operation_id=e.operation_id
 AND t.client_id=a.client_id AND t.candidate_id=a.candidate_id AND t.state='completed' AND t.progress=1 AND t.downloaded_bytes=t.total_bytes
 AND jsonb_array_length(a.snapshot->'complete_seasons')=1 AND (a.snapshot->'complete_seasons'->0->>'season')::int>0
 AND prep.proof->>'kind'='complete_tv_prepared_v2' AND prep.proof->>'save_path'=t.save_path AND prep.info_hash=t.info_hash
 AND inv.operation_id=e.operation_id AND inv.client_id=t.client_id AND inv.info_hash=t.info_hash AND inv.total_bytes=t.total_bytes AND inv.content_path=t.save_path
 AND inv.client_stamp=a.snapshot->>'client_stamp' AND inv.files=e.tracking_result->'files'
 AND j.kind='complete_tv_coverage_tracking' AND j.state='succeeded' AND j.result=e.tracking_result
 AND j.payload->>'task_id'=t.id::text AND j.payload->>'operation_id'=t.operation_id::text
 AND r.id=a.root_id AND r.media_type='tv' AND r.health='online' AND r.baseline_completed AND r.path=p.root_path AND r.generation=p.root_generation
 AND r.generation=(a.snapshot->>'root_generation')::bigint AND sub.user_id=a.actor_id AND sub.state='active' AND sub.root_id=r.id
 AND sub.media_id=(a.snapshot->>'media_id')::uuid AND sub.intent_version=(a.snapshot->>'intent_version')::bigint
 AND sub.quality_policy_id=a.snapshot->>'policy_id' AND sub.quality_policy_revision=(a.snapshot->>'policy_revision')::bigint AND pol.current_revision=sub.quality_policy_revision
 AND sub.downloader_id=a.client_id AND COALESCE(sub.episode_mapping_revision,0)=COALESCE((a.snapshot->'mapping'->>'mapping_revision')::bigint,0)
 AND (SELECT count(*) FROM transfer_step s WHERE s.plan_id=plan)=(SELECT count(*) FROM complete_tv_submission_unit u WHERE u.task_id=t.id)
 AND (SELECT count(DISTINCT unit_id) FROM transfer_step s WHERE s.plan_id=plan)=jsonb_array_length(a.snapshot->'units')
 AND NOT EXISTS(
  SELECT 1 FROM complete_tv_submission_unit frozen LEFT JOIN transfer_step s ON s.plan_id=plan AND s.unit_id=frozen.unit_id
  LEFT JOIN media_unit u ON u.id=frozen.unit_id LEFT JOIN media m ON m.id=u.media_id
  LEFT JOIN media_unit_identity identity ON identity.unit_id=u.id AND identity.namespace='tmdb.episode'
  WHERE frozen.task_id=t.id AND (s.id IS NULL OR u.id IS NULL OR u.retired OR u.unit_key<>frozen.unit_key OR u.metadata_version<>frozen.unit_version
   OR identity.external_id IS DISTINCT FROM frozen.provider_episode_id OR m.metadata_version<>(a.snapshot->>'media_version')::bigint OR m.media_type<>'tv'
   OR u.media_id<>sub.media_id OR u.season_number<>(a.snapshot->'complete_seasons'->0->>'season')::int OR u.season_number<1 OR u.episode_number<1 OR s.source_size<>frozen.video_size
   OR s.action<>'hardlink' OR NOT EXISTS(SELECT 1 FROM wanted_item w WHERE w.subscription_id=sub.id AND w.unit_id=u.id AND NOT w.is_skipped)
   OR NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=e.operation_id AND d.root_id=r.id AND d.unit_id=u.id AND d.actor_type='user' AND d.actor_id=a.actor_id::text AND d.state IN('pending','imported')))
 )
),false);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION pulse_complete_tv_transfer_valid(plan UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
SELECT COALESCE((SELECT pulse_complete_tv_transfer_targets_valid(plan)
 AND (e.baseline=pulse_complete_tv_transfer_state(e.operation_id,plan)
 OR (p.state='completed' AND EXISTS(SELECT 1 FROM download_task t WHERE t.id=e.task_id AND t.organization_state='completed' AND t.transfer_plan_id=plan)
 AND NOT EXISTS(SELECT 1 FROM transfer_step s WHERE s.plan_id=plan AND s.state<>'completed')
 AND NOT EXISTS(SELECT 1 FROM acquisition_reservation a WHERE a.operation_id=e.operation_id AND a.released_at IS NULL)
 AND e.baseline-'lanes'=pulse_complete_tv_transfer_state(e.operation_id,plan)-'lanes'
 AND e.baseline->'lanes'=(SELECT jsonb_agg(to_jsonb(a)||jsonb_build_object('released_at',NULL) ORDER BY a.id) FROM acquisition_reservation a WHERE a.operation_id=e.operation_id)))
 AND NOT EXISTS(SELECT 1 FROM transfer_step s WHERE s.plan_id=plan AND (s.target_evidence IS NULL OR s.target_evidence->>'complete_tv_plan_id' IS DISTINCT FROM plan::text
 OR (s.state='completed' AND (
 NOT EXISTS(SELECT 1 FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id AND fu.unit_id=s.unit_id WHERE f.id=s.target_file_id AND f.root_id=p.root_id AND f.rel_path=s.target_rel AND f.size_bytes=s.source_size AND f.mtime_ns=s.source_mtime AND f.retired_at IS NULL AND fu.retired_at IS NULL AND f.file_fingerprint='stat.v1:'||s.source_inode::text||':'||s.source_size::text||':'||s.source_mtime::text)
 OR (SELECT count(*) FROM library_file_unit fu WHERE fu.file_id=s.target_file_id)<>1
 OR (SELECT count(*) FROM unit_fact fact WHERE fact.source_file_id=s.target_file_id)<>1
 OR NOT EXISTS(SELECT 1 FROM unit_fact fact WHERE fact.source_file_id=s.target_file_id AND fact.unit_id=s.unit_id AND fact.root_id=p.root_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND fact.quality_profile=s.quality_profile)
 OR NOT EXISTS(SELECT 1 FROM unit_reservation ur WHERE ur.operation_id=e.operation_id AND ur.unit_id=s.unit_id AND ur.root_id=p.root_id AND ur.state='consumed')
 OR NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=e.operation_id AND d.unit_id=s.unit_id AND d.root_id=p.root_id AND d.state='imported')
 )))) FROM complete_tv_transfer_evidence e JOIN transfer_plan p ON p.id=e.plan_id WHERE e.plan_id=plan),false);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_transfer_target_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF OLD.target_evidence IS NOT NULL AND (NEW.target_evidence IS DISTINCT FROM OLD.target_evidence OR NEW.unit_id IS DISTINCT FROM OLD.unit_id OR NEW.plan_id IS DISTINCT FROM OLD.plan_id) THEN RAISE EXCEPTION 'frozen transfer target cannot be rewritten';END IF;
 IF (OLD.target_evidence IS NULL AND NEW.target_evidence IS NOT NULL) OR (NEW.state='completed' AND OLD.state<>'completed') THEN
 IF NEW.target_evidence IS NULL THEN RAISE EXCEPTION 'missing transfer target evidence';END IF;
 IF NOT COALESCE(((NEW.target_evidence->>'complete_tv_plan_id'=NEW.plan_id::text AND pulse_complete_tv_transfer_targets_valid(NEW.plan_id)
 AND NEW.target_evidence->>'unit_id'=NEW.unit_id::text AND NEW.target_evidence->>'inventory_source_path'=NEW.source_path
 AND (NEW.target_evidence->>'inventory_size_bytes')::bigint=NEW.source_size
 AND EXISTS(SELECT 1 FROM media_unit full_unit JOIN media full_media ON full_media.id=full_unit.media_id JOIN complete_tv_transfer_evidence full_evidence ON full_evidence.plan_id=NEW.plan_id
 WHERE full_unit.id=NEW.unit_id AND NEW.target_evidence->>'unit_key'=full_unit.unit_key
 AND (NEW.target_evidence->>'unit_version')::bigint=full_unit.metadata_version AND NEW.target_evidence->>'media_id'=full_media.id::text
 AND (NEW.target_evidence->>'media_version')::bigint=full_media.metadata_version AND NEW.target_evidence->>'inventory_task_id'=full_evidence.task_id::text
 AND NEW.target_evidence->>'parser_version' ~ '^episode[.]v[0-9]+$')) OR EXISTS(SELECT 1 FROM media_unit u JOIN media m ON m.id=u.media_id JOIN transfer_plan plan ON plan.id=NEW.plan_id JOIN library_root root ON root.id=plan.root_id
 WHERE u.id=NEW.unit_id AND u.id=(NEW.target_evidence->>'unit_id')::uuid AND u.media_id=(NEW.target_evidence->>'media_id')::uuid AND NEW.target_evidence->>'unit_key'=u.unit_key AND NOT u.retired AND u.metadata_version=(NEW.target_evidence->>'unit_version')::bigint AND m.metadata_version=(NEW.target_evidence->>'media_version')::bigint AND NEW.target_evidence->>'parser_version' ~ '^episode[.]v[0-9]+$'
 AND ((u.unit_key='movie' AND m.media_type='movie' AND root.media_type='movie' AND NOT (NEW.target_evidence ? 'episode_operation_id'))
 OR (m.media_type='tv' AND root.media_type='tv' AND root.health='online' AND root.baseline_completed AND root.generation=plan.root_generation AND root.path=plan.root_path AND EXISTS(
 SELECT 1 FROM episode_execution_authority e JOIN episode_candidate_review review ON review.id=e.review_id JOIN download_file_inventory inv ON inv.operation_id=e.operation_id JOIN download_task task ON task.id=inv.task_id JOIN subscription sub ON sub.id=e.subscription_id JOIN policy pol ON pol.id=sub.quality_policy_id JOIN media_unit_identity identity ON identity.unit_id=u.id AND identity.namespace='tmdb.episode' JOIN release_candidate candidate ON candidate.id=e.candidate_id JOIN indexer source ON source.id=candidate.indexer_id
 WHERE e.operation_id=plan.operation_id AND e.operation_id=(NEW.target_evidence->>'episode_operation_id')::uuid AND inv.task_id=(NEW.target_evidence->>'inventory_task_id')::uuid AND e.unit_id=u.id AND e.root_id=root.id AND e.actor_id=sub.user_id AND e.client_id=task.client_id AND e.candidate_id=task.candidate_id AND task.operation_id=e.operation_id AND task.state='completed' AND task.info_hash=inv.info_hash AND task.total_bytes=inv.total_bytes AND task.content_path=inv.content_path AND jsonb_array_length(inv.files)=1 AND (inv.files->0->>'progress')::numeric=1 AND (inv.files->0->>'size_bytes')::bigint=task.total_bytes
 AND ((e.snapshot->>'kind'='reviewed_standard_episode' AND e.snapshot->>'mapping_mode'='standard') OR (e.snapshot->>'kind'='reviewed_mapped_episode' AND e.snapshot->>'mapping_mode' IN ('absolute','manual') AND (e.snapshot->>'source_number')::int BETWEEN 1 AND 9999 AND jsonb_typeof(e.snapshot->'mapping_rows')='array' AND EXISTS(SELECT 1 FROM episode_mapping_revision mapped WHERE mapped.subscription_id=sub.id AND mapped.revision=sub.episode_mapping_revision AND mapped.mode=e.snapshot->>'mapping_mode' AND mapped.media_id=m.id AND mapped.media_version=m.metadata_version AND mapped.source_hash=e.snapshot->>'mapping_source_hash' AND mapped.mappings=e.snapshot->'mapping_rows' AND ((mapped.mode='manual' AND mapped.valid_until IS NULL AND e.snapshot->>'mapping_valid_until' IS NULL) OR (mapped.mode='absolute' AND mapped.valid_until>clock_timestamp() AND mapped.valid_until=(e.snapshot->>'mapping_valid_until')::timestamptz)) AND EXISTS(SELECT 1 FROM jsonb_array_elements(mapped.mappings) chosen WHERE (chosen->>'source_number')::int=(e.snapshot->>'source_number')::int AND (chosen->>'unit_id')::uuid=u.id AND (chosen->>'unit_version')::bigint=u.metadata_version AND chosen->>'unit_key'=u.unit_key AND (chosen->>'episode_id')::bigint=identity.external_id) AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(mapped.mappings) row LEFT JOIN media_unit target ON target.id=(row->>'unit_id')::uuid LEFT JOIN media_unit_identity provider ON provider.unit_id=target.id AND provider.namespace='tmdb.episode' WHERE target.id IS NULL OR target.retired OR target.media_id<>m.id OR target.metadata_version<>(row->>'unit_version')::bigint OR target.unit_key<>row->>'unit_key' OR provider.external_id IS NULL OR provider.external_id<>(row->>'episode_id')::bigint)))) AND e.snapshot->>'parser_version'=NEW.target_evidence->>'parser_version' AND e.snapshot->>'client_stamp'=inv.client_stamp AND (e.snapshot->>'unit_version')::bigint=u.metadata_version AND (e.snapshot->>'media_version')::bigint=m.metadata_version AND (e.snapshot->>'episode_id')::bigint=identity.external_id AND (e.snapshot->>'root_generation')::bigint=root.generation AND sub.state='active' AND sub.media_id=m.id AND sub.root_id=root.id AND sub.intent_version=(e.snapshot->>'intent_version')::bigint AND sub.quality_policy_id=e.snapshot->>'policy_id' AND sub.quality_policy_revision=(e.snapshot->>'policy_revision')::bigint AND pol.current_revision=sub.quality_policy_revision AND COALESCE(sub.episode_mapping_revision,0)=(e.snapshot->>'mapping_revision')::bigint AND sub.downloader_id=e.client_id
 AND review.input_stamp=e.snapshot->>'review_input_stamp' AND review.unit_id=u.id AND review.media_id=m.id AND review.unit_version=u.metadata_version AND review.media_version=m.metadata_version AND review.intent_version=sub.intent_version AND review.parser_version=e.snapshot->>'parser_version' AND source.id=(e.snapshot->>'indexer_id')::uuid AND source.health='online' AND source.config_version=(e.snapshot->>'source_version')::bigint AND candidate.source_config_version=source.config_version
 AND EXISTS(SELECT 1 FROM wanted_item w WHERE w.subscription_id=sub.id AND w.unit_id=u.id AND NOT w.is_skipped)
 AND EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=e.operation_id AND d.root_id=root.id AND d.unit_id=u.id AND d.state='pending' AND d.actor_id=e.actor_id::text AND d.actor_type='user')
 ))))  ),false) THEN RAISE EXCEPTION 'transfer target evidence does not match canonical input';END IF;
 END IF;RETURN NEW;END$$;
-- +goose StatementEnd

-- Freeze only the dedicated full-set branch; preserve existing single/movie behavior.
-- +goose StatementBegin
CREATE FUNCTION pulse_complete_tv_plan_frozen() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF EXISTS(SELECT 1 FROM complete_tv_transfer_evidence WHERE plan_id=OLD.id) THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'complete TV plan history is immutable'; END IF;
  IF ROW(NEW.operation_id,NEW.root_id,NEW.root_path,NEW.root_generation,NEW.root_device,NEW.root_inode,NEW.old_file_id,NEW.strategy,NEW.requires_approval)
   IS DISTINCT FROM ROW(OLD.operation_id,OLD.root_id,OLD.root_path,OLD.root_generation,OLD.root_device,OLD.root_inode,OLD.old_file_id,OLD.strategy,OLD.requires_approval) THEN
   RAISE EXCEPTION 'complete TV frozen plan cannot be rewritten';
  END IF;
 END IF; IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END$$;
-- +goose StatementEnd
CREATE TRIGGER complete_tv_plan_frozen BEFORE UPDATE OR DELETE ON transfer_plan FOR EACH ROW EXECUTE FUNCTION pulse_complete_tv_plan_frozen();
-- +goose StatementBegin
CREATE FUNCTION pulse_complete_tv_step_frozen() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF EXISTS(SELECT 1 FROM complete_tv_transfer_evidence WHERE plan_id=COALESCE(NEW.plan_id,OLD.plan_id)) THEN
  IF TG_OP IN('INSERT','DELETE') THEN RAISE EXCEPTION 'complete TV frozen step set cannot change'; END IF;
  IF OLD.target_evidence IS NOT NULL AND (
   ROW(NEW.id,NEW.plan_id,NEW.unit_id,NEW.source_path,NEW.target_path,NEW.action,NEW.step_order,NEW.source_size,NEW.source_mtime,NEW.source_root,NEW.source_rel,NEW.target_rel,NEW.source_device,NEW.source_inode,NEW.quality_profile,NEW.target_evidence)
   IS DISTINCT FROM ROW(OLD.id,OLD.plan_id,OLD.unit_id,OLD.source_path,OLD.target_path,OLD.action,OLD.step_order,OLD.source_size,OLD.source_mtime,OLD.source_root,OLD.source_rel,OLD.target_rel,OLD.source_device,OLD.source_inode,OLD.quality_profile,OLD.target_evidence)
   OR OLD.state='completed' AND ROW(NEW.state,NEW.target_file_id) IS DISTINCT FROM ROW(OLD.state,OLD.target_file_id)
  ) THEN RAISE EXCEPTION 'complete TV frozen step cannot be rewritten'; END IF;
 END IF; RETURN COALESCE(NEW,OLD);
END$$;
-- +goose StatementEnd
CREATE TRIGGER complete_tv_step_frozen BEFORE INSERT OR UPDATE OR DELETE ON transfer_step FOR EACH ROW EXECUTE FUNCTION pulse_complete_tv_step_frozen();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN
 LOCK TABLE complete_tv_transfer_evidence IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM complete_tv_transfer_evidence) THEN RAISE EXCEPTION 'complete TV organization history cannot be discarded'; END IF;
END$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_transfer_target_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF OLD.target_evidence IS NOT NULL AND (NEW.target_evidence IS DISTINCT FROM OLD.target_evidence OR NEW.unit_id IS DISTINCT FROM OLD.unit_id OR NEW.plan_id IS DISTINCT FROM OLD.plan_id) THEN RAISE EXCEPTION 'frozen transfer target cannot be rewritten';END IF;
 IF (OLD.target_evidence IS NULL AND NEW.target_evidence IS NOT NULL) OR (NEW.state='completed' AND OLD.state<>'completed') THEN
 IF NEW.target_evidence IS NULL THEN RAISE EXCEPTION 'missing transfer target evidence';END IF;
 IF NOT EXISTS(SELECT 1 FROM media_unit u JOIN media m ON m.id=u.media_id JOIN transfer_plan plan ON plan.id=NEW.plan_id JOIN library_root root ON root.id=plan.root_id
 WHERE u.id=NEW.unit_id AND u.id=(NEW.target_evidence->>'unit_id')::uuid AND u.media_id=(NEW.target_evidence->>'media_id')::uuid AND NEW.target_evidence->>'unit_key'=u.unit_key AND NOT u.retired AND u.metadata_version=(NEW.target_evidence->>'unit_version')::bigint AND m.metadata_version=(NEW.target_evidence->>'media_version')::bigint AND NEW.target_evidence->>'parser_version' ~ '^episode[.]v[0-9]+$'
 AND ((u.unit_key='movie' AND m.media_type='movie' AND root.media_type='movie' AND NOT (NEW.target_evidence ? 'episode_operation_id'))
 OR (m.media_type='tv' AND root.media_type='tv' AND root.health='online' AND root.baseline_completed AND root.generation=plan.root_generation AND root.path=plan.root_path AND EXISTS(
 SELECT 1 FROM episode_execution_authority e JOIN episode_candidate_review review ON review.id=e.review_id JOIN download_file_inventory inv ON inv.operation_id=e.operation_id JOIN download_task task ON task.id=inv.task_id JOIN subscription sub ON sub.id=e.subscription_id JOIN policy pol ON pol.id=sub.quality_policy_id JOIN media_unit_identity identity ON identity.unit_id=u.id AND identity.namespace='tmdb.episode' JOIN release_candidate candidate ON candidate.id=e.candidate_id JOIN indexer source ON source.id=candidate.indexer_id
 WHERE e.operation_id=plan.operation_id AND e.operation_id=(NEW.target_evidence->>'episode_operation_id')::uuid AND inv.task_id=(NEW.target_evidence->>'inventory_task_id')::uuid AND e.unit_id=u.id AND e.root_id=root.id AND e.actor_id=sub.user_id AND e.client_id=task.client_id AND e.candidate_id=task.candidate_id AND task.operation_id=e.operation_id AND task.state='completed' AND task.info_hash=inv.info_hash AND task.total_bytes=inv.total_bytes AND task.content_path=inv.content_path AND jsonb_array_length(inv.files)=1 AND (inv.files->0->>'progress')::numeric=1 AND (inv.files->0->>'size_bytes')::bigint=task.total_bytes
 AND ((e.snapshot->>'kind'='reviewed_standard_episode' AND e.snapshot->>'mapping_mode'='standard') OR (e.snapshot->>'kind'='reviewed_mapped_episode' AND e.snapshot->>'mapping_mode' IN ('absolute','manual') AND (e.snapshot->>'source_number')::int BETWEEN 1 AND 9999 AND jsonb_typeof(e.snapshot->'mapping_rows')='array' AND EXISTS(SELECT 1 FROM episode_mapping_revision mapped WHERE mapped.subscription_id=sub.id AND mapped.revision=sub.episode_mapping_revision AND mapped.mode=e.snapshot->>'mapping_mode' AND mapped.media_id=m.id AND mapped.media_version=m.metadata_version AND mapped.source_hash=e.snapshot->>'mapping_source_hash' AND mapped.mappings=e.snapshot->'mapping_rows' AND ((mapped.mode='manual' AND mapped.valid_until IS NULL AND e.snapshot->>'mapping_valid_until' IS NULL) OR (mapped.mode='absolute' AND mapped.valid_until>clock_timestamp() AND mapped.valid_until=(e.snapshot->>'mapping_valid_until')::timestamptz)) AND EXISTS(SELECT 1 FROM jsonb_array_elements(mapped.mappings) chosen WHERE (chosen->>'source_number')::int=(e.snapshot->>'source_number')::int AND (chosen->>'unit_id')::uuid=u.id AND (chosen->>'unit_version')::bigint=u.metadata_version AND chosen->>'unit_key'=u.unit_key AND (chosen->>'episode_id')::bigint=identity.external_id) AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(mapped.mappings) row LEFT JOIN media_unit target ON target.id=(row->>'unit_id')::uuid LEFT JOIN media_unit_identity provider ON provider.unit_id=target.id AND provider.namespace='tmdb.episode' WHERE target.id IS NULL OR target.retired OR target.media_id<>m.id OR target.metadata_version<>(row->>'unit_version')::bigint OR target.unit_key<>row->>'unit_key' OR provider.external_id IS NULL OR provider.external_id<>(row->>'episode_id')::bigint)))) AND e.snapshot->>'parser_version'=NEW.target_evidence->>'parser_version' AND e.snapshot->>'client_stamp'=inv.client_stamp AND (e.snapshot->>'unit_version')::bigint=u.metadata_version AND (e.snapshot->>'media_version')::bigint=m.metadata_version AND (e.snapshot->>'episode_id')::bigint=identity.external_id AND (e.snapshot->>'root_generation')::bigint=root.generation AND sub.state='active' AND sub.media_id=m.id AND sub.root_id=root.id AND sub.intent_version=(e.snapshot->>'intent_version')::bigint AND sub.quality_policy_id=e.snapshot->>'policy_id' AND sub.quality_policy_revision=(e.snapshot->>'policy_revision')::bigint AND pol.current_revision=sub.quality_policy_revision AND COALESCE(sub.episode_mapping_revision,0)=(e.snapshot->>'mapping_revision')::bigint AND sub.downloader_id=e.client_id
 AND review.input_stamp=e.snapshot->>'review_input_stamp' AND review.unit_id=u.id AND review.media_id=m.id AND review.unit_version=u.metadata_version AND review.media_version=m.metadata_version AND review.intent_version=sub.intent_version AND review.parser_version=e.snapshot->>'parser_version' AND source.id=(e.snapshot->>'indexer_id')::uuid AND source.health='online' AND source.config_version=(e.snapshot->>'source_version')::bigint AND candidate.source_config_version=source.config_version
 AND EXISTS(SELECT 1 FROM wanted_item w WHERE w.subscription_id=sub.id AND w.unit_id=u.id AND NOT w.is_skipped)
 AND EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=e.operation_id AND d.root_id=root.id AND d.unit_id=u.id AND d.state='pending' AND d.actor_id=e.actor_id::text AND d.actor_type='user')
 )))) THEN RAISE EXCEPTION 'transfer target evidence does not match canonical input';END IF;
 END IF;RETURN NEW;END$$;
-- +goose StatementEnd
DROP TRIGGER complete_tv_plan_frozen ON transfer_plan;
DROP TRIGGER complete_tv_step_frozen ON transfer_step;
DROP FUNCTION pulse_complete_tv_plan_frozen();
DROP FUNCTION pulse_complete_tv_step_frozen();
DROP FUNCTION pulse_complete_tv_transfer_valid(uuid);
DROP FUNCTION pulse_complete_tv_transfer_targets_valid(uuid);
DROP FUNCTION pulse_complete_tv_transfer_state(uuid,uuid);
DROP TABLE complete_tv_transfer_evidence;
