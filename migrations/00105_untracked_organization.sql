-- +goose Up
CREATE TABLE untracked_organization_operation (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 scope_id UUID NOT NULL REFERENCES untracked_directory_scope(id) ON DELETE RESTRICT,
 file_id UUID NOT NULL REFERENCES untracked_discovered_file(id) ON DELETE RESTRICT,
 confirmation_id UUID NOT NULL UNIQUE REFERENCES untracked_review_confirmation(id) ON DELETE RESTRICT,
 plan_id UUID NOT NULL UNIQUE REFERENCES transfer_plan(id) DEFERRABLE INITIALLY DEFERRED,
 step_id UUID NOT NULL UNIQUE REFERENCES transfer_step(id) DEFERRABLE INITIALLY DEFERRED,
 job_id UUID NOT NULL UNIQUE REFERENCES job(id) DEFERRABLE INITIALLY DEFERRED,
 command_key TEXT NOT NULL CHECK(command_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,160}$'),
 input_hash TEXT NOT NULL CHECK(input_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 material JSONB NOT NULL CHECK(jsonb_typeof(material)='object'),
 receipt JSONB NOT NULL CHECK(jsonb_typeof(receipt)='object'),
 UNIQUE(scope_id,file_id,command_key), UNIQUE(id,job_id), UNIQUE(id,step_id)
);
CREATE TABLE untracked_organization_unit (
 operation_id UUID NOT NULL REFERENCES untracked_organization_operation(id) DEFERRABLE INITIALLY DEFERRED,
 unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
 media_id UUID NOT NULL REFERENCES media(id) ON DELETE RESTRICT,
 metadata_version BIGINT NOT NULL CHECK(metadata_version>0),
 media_metadata_version BIGINT NOT NULL CHECK(media_metadata_version>0),
 unit_key TEXT NOT NULL,
 tmdb_episode_id BIGINT NOT NULL CHECK(tmdb_episode_id>=0),
 candidate_unit JSONB NOT NULL CHECK(jsonb_typeof(candidate_unit)='object'),
 media_identity JSONB NOT NULL CHECK(jsonb_typeof(media_identity)='object'),
 PRIMARY KEY(operation_id,unit_id), UNIQUE(operation_id,unit_key)
);
CREATE TABLE untracked_organization_permit (
 operation_id UUID PRIMARY KEY REFERENCES untracked_organization_operation(id) ON DELETE RESTRICT,
 job_id UUID NOT NULL,
 job_owner TEXT NOT NULL CHECK(job_owner<>''), job_token BIGINT NOT NULL CHECK(job_token>0),
 execution_owner TEXT NOT NULL CHECK(execution_owner<>''), execution_token BIGINT NOT NULL CHECK(execution_token>0),
 content_size BIGINT NOT NULL CHECK(content_size>=0),
 content_sha256 BYTEA NOT NULL CHECK(octet_length(content_sha256)=32),
 proof_hash TEXT NOT NULL CHECK(proof_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 issued_at TIMESTAMPTZ NOT NULL,
 FOREIGN KEY(operation_id,job_id) REFERENCES untracked_organization_operation(id,job_id),
 UNIQUE(operation_id,job_id,job_owner,job_token)
);
CREATE TABLE untracked_organization_observation (
 id UUID PRIMARY KEY,
 operation_id UUID NOT NULL REFERENCES untracked_organization_permit(operation_id) ON DELETE RESTRICT,
 job_id UUID NOT NULL, job_owner TEXT NOT NULL CHECK(job_owner<>''), job_token BIGINT NOT NULL CHECK(job_token>0),
 result JSONB NOT NULL CHECK(jsonb_typeof(result)='object'), recorded_at TIMESTAMPTZ NOT NULL,
 FOREIGN KEY(operation_id,job_id) REFERENCES untracked_organization_operation(id,job_id),
 UNIQUE(id,operation_id)
);
CREATE TABLE untracked_organization_completion (
 operation_id UUID PRIMARY KEY REFERENCES untracked_organization_operation(id) ON DELETE RESTRICT,
 step_id UUID NOT NULL UNIQUE REFERENCES transfer_step(id) ON DELETE RESTRICT,
 file_id UUID NOT NULL REFERENCES library_file(id) ON DELETE RESTRICT,
 observation_id UUID NOT NULL UNIQUE,
 result JSONB NOT NULL CHECK(jsonb_typeof(result)='object'), recorded_at TIMESTAMPTZ NOT NULL,
 FOREIGN KEY(operation_id,step_id) REFERENCES untracked_organization_operation(id,step_id),
 FOREIGN KEY(observation_id,operation_id) REFERENCES untracked_organization_observation(id,operation_id)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_organization_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'untracked organization history is protected'; END $$;
-- +goose StatementEnd
CREATE TRIGGER untracked_organization_operation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_organization_operation FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_history_guard();
CREATE TRIGGER untracked_organization_unit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_organization_unit FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_history_guard();
CREATE TRIGGER untracked_organization_permit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_organization_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_history_guard();
CREATE TRIGGER untracked_organization_observation_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_organization_observation FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_history_guard();
CREATE TRIGGER untracked_organization_completion_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_organization_completion FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_history_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_organization_consistency() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE o untracked_organization_operation; c untracked_review_confirmation; p transfer_plan; s transfer_step; m jsonb; op uuid;
BEGIN
 IF TG_TABLE_NAME='untracked_organization_operation' THEN op:=NEW.id; ELSE op:=NEW.operation_id; END IF;
 SELECT * INTO STRICT o FROM untracked_organization_operation WHERE id=op;
 SELECT * INTO STRICT c FROM untracked_review_confirmation WHERE id=o.confirmation_id;
 SELECT * INTO STRICT p FROM transfer_plan WHERE id=o.plan_id;
 SELECT * INTO STRICT s FROM transfer_step WHERE id=o.step_id;
 m:=o.material->'confirmation'->'material';
 IF c.owner_id<>o.owner_id OR c.scope_id<>o.scope_id OR c.file_id<>o.file_id
  OR c.receipt IS DISTINCT FROM o.material->'confirmation'
  OR c.receipt->>'state' IS DISTINCT FROM 'confirmed_pending_organization'
  OR c.receipt->'execution_allowed' IS DISTINCT FROM 'false'::jsonb
  OR o.material->'operation' IS DISTINCT FROM o.receipt->'operation'
  OR o.receipt->>'command_key' IS DISTINCT FROM o.command_key OR o.receipt->>'state' IS DISTINCT FROM 'queued'
  OR o.receipt->'execution_allowed' IS DISTINCT FROM 'false'::jsonb
  OR o.material->'operation'->>'id' IS DISTINCT FROM o.id::text
  OR o.material->'operation'->>'actor_id' IS DISTINCT FROM o.owner_id::text
  OR o.material->'operation'->>'scope_id' IS DISTINCT FROM o.scope_id::text
  OR o.material->'operation'->>'file_id' IS DISTINCT FROM o.file_id::text
  OR o.material->'operation'->>'confirmation_id' IS DISTINCT FROM o.confirmation_id::text
  OR o.material->'operation'->>'plan_id' IS DISTINCT FROM o.plan_id::text
  OR o.material->'operation'->>'step_id' IS DISTINCT FROM o.step_id::text
  OR o.material->'operation'->>'job_id' IS DISTINCT FROM o.job_id::text
  OR p.operation_id<>o.id OR p.root_id<>(m->'scope'->>'target_root_id')::uuid
  OR NOT p.requires_approval OR p.strategy IS DISTINCT FROM m->'input'->>'action'
  OR p.root_path IS DISTINCT FROM m->'scope'->>'target_root_path'
  OR p.root_generation IS DISTINCT FROM (m->'scope'->>'target_root_generation')::bigint
  OR p.root_device IS DISTINCT FROM (m->'scope'->'target_identity'->>'Device')::bigint
  OR p.root_inode IS DISTINCT FROM (m->'scope'->'target_identity'->>'Inode')::bigint OR p.old_file_id IS NOT NULL
  OR s.plan_id<>p.id OR s.action IS DISTINCT FROM p.strategy OR s.step_order<>0
  OR s.source_root IS DISTINCT FROM m->'scope'->>'local_root'
  OR s.source_rel IS DISTINCT FROM m->'file'->>'RelativePath'
  OR s.target_rel IS DISTINCT FROM m->'input'->>'target_relative_path'
  OR s.source_path IS DISTINCT FROM rtrim(s.source_root,'/')||'/'||s.source_rel
  OR s.target_path IS DISTINCT FROM rtrim(p.root_path,'/')||'/'||s.target_rel
  OR s.source_size IS DISTINCT FROM (m->'file'->'Identity'->>'Size')::bigint
  OR s.source_mtime IS DISTINCT FROM (m->'file'->'Identity'->>'Mtime')::bigint
  OR s.source_device IS DISTINCT FROM (m->'file'->'Identity'->>'Device')::bigint
  OR s.source_inode IS DISTINCT FROM (m->'file'->'Identity'->>'Inode')::bigint
  OR s.target_evidence->>'untracked_organization_id' IS DISTINCT FROM o.id::text
  OR s.target_evidence ?| ARRAY['file_action_id','file_action_operation_id','episode_operation_id','complete_tv_operation_id','automatic_tv_operation_id','download_operation_id']
  OR (SELECT count(*) FROM transfer_step WHERE plan_id=p.id)<>1
  OR NOT EXISTS(SELECT 1 FROM job j WHERE j.id=o.job_id AND j.kind='untracked_organization' AND j.max_attempts=3 AND j.payload=jsonb_build_object('operation_id',o.id))
  OR NOT EXISTS(SELECT 1 FROM untracked_organization_unit u WHERE u.operation_id=o.id AND u.unit_id=s.unit_id)
  OR (SELECT count(*) FROM untracked_organization_unit WHERE operation_id=o.id)<>jsonb_array_length(m->'review'->'evidence'->'candidate'->'units') THEN
  RAISE EXCEPTION 'untracked organization graph mismatch';
 END IF;
 IF TG_TABLE_NAME='untracked_organization_unit' THEN
 IF NOT EXISTS(
  SELECT 1 FROM media_unit u JOIN media md ON md.id=u.media_id JOIN media_identity i ON i.media_id=md.id
  WHERE u.id=NEW.unit_id AND u.media_id=NEW.media_id AND NOT u.retired AND u.unit_key=NEW.unit_key
   AND u.metadata_version=NEW.metadata_version AND md.metadata_version=NEW.media_metadata_version
   AND i.namespace=NEW.media_identity->>'namespace' AND i.external_id=NEW.media_identity->>'external_id'
   AND NEW.candidate_unit IN (SELECT value FROM jsonb_array_elements(m->'review'->'evidence'->'candidate'->'units'))
   AND ((md.media_type='movie' AND u.unit_key='movie' AND NEW.tmdb_episode_id=0)
    OR (md.media_type='tv' AND EXISTS(SELECT 1 FROM media_unit_identity ei WHERE ei.unit_id=u.id AND ei.namespace='tmdb.episode' AND ei.external_id=NEW.tmdb_episode_id AND ei.tmdb_series_id=(NEW.media_identity->>'external_id')::bigint)))) THEN
  RAISE EXCEPTION 'untracked organization canonical unit mismatch';
 END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER untracked_organization_operation_consistency AFTER INSERT ON untracked_organization_operation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_consistency();
CREATE CONSTRAINT TRIGGER untracked_organization_unit_consistency AFTER INSERT ON untracked_organization_unit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_consistency();
-- All four actions freeze the same fields. State, execution lease and completion remain writable.
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_organization_transfer_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE guarded boolean;
BEGIN
 IF TG_OP='TRUNCATE' THEN
  IF EXISTS(SELECT 1 FROM untracked_organization_operation) THEN RAISE EXCEPTION 'untracked organization transfer history is protected'; END IF;
  RETURN NULL;
 END IF;
 IF TG_TABLE_NAME='transfer_plan' THEN
  SELECT EXISTS(SELECT 1 FROM untracked_organization_operation WHERE plan_id=OLD.id) INTO guarded;
  IF guarded AND (TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['state','updated_at','finished_at','execution_owner','execution_token','lease_until']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','updated_at','finished_at','execution_owner','execution_token','lease_until'])) THEN
   RAISE EXCEPTION 'untracked organization plan is frozen';
  END IF;
 ELSE
  IF TG_OP='INSERT' THEN
   IF EXISTS(SELECT 1 FROM untracked_organization_operation WHERE plan_id=NEW.plan_id AND step_id<>NEW.id) THEN RAISE EXCEPTION 'untracked organization has one file step'; END IF;
   RETURN NEW;
  END IF;
  SELECT EXISTS(SELECT 1 FROM untracked_organization_operation WHERE plan_id=OLD.plan_id OR step_id=OLD.id) INTO guarded;
  IF guarded AND (TG_OP='DELETE' OR (to_jsonb(NEW)-ARRAY['state','updated_at','finished_at','target_file_id','error']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','updated_at','finished_at','target_file_id','error'])) THEN
   RAISE EXCEPTION 'untracked organization step is frozen';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER untracked_organization_plan_frozen BEFORE UPDATE OR DELETE ON transfer_plan FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_transfer_frozen();
CREATE TRIGGER untracked_organization_step_frozen BEFORE INSERT OR UPDATE OR DELETE ON transfer_step FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_transfer_frozen();
CREATE TRIGGER untracked_organization_plan_truncate BEFORE TRUNCATE ON transfer_plan FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_transfer_frozen();
CREATE TRIGGER untracked_organization_step_truncate BEFORE TRUNCATE ON transfer_step FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_organization_transfer_frozen();
-- Independent of the legacy download/automatic-TV target trigger dispatch.
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_organization_deferred_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op uuid; o untracked_organization_operation; p transfer_plan; s transfer_step;
 c untracked_review_confirmation; u untracked_organization_unit; j job;
 permit untracked_organization_permit; done untracked_organization_completion;
 observation untracked_organization_observation; m jsonb; expected jsonb; content jsonb; digest bytea;
BEGIN
 IF TG_TABLE_NAME='transfer_step' THEN
  SELECT id INTO op FROM untracked_organization_operation WHERE step_id=NEW.id OR plan_id=NEW.plan_id;
  IF op IS NULL THEN
   IF NEW.target_evidence ? 'untracked_organization_id' THEN RAISE EXCEPTION 'orphan untracked organization marker'; END IF;
   RETURN NULL;
  END IF;
 ELSIF TG_TABLE_NAME='transfer_plan' THEN
  SELECT id INTO op FROM untracked_organization_operation WHERE plan_id=NEW.id;
  IF op IS NULL THEN RETURN NULL; END IF;
 ELSIF TG_TABLE_NAME='untracked_organization_operation' THEN op:=NEW.id;
 ELSE op:=NEW.operation_id;
 END IF;
 SELECT * INTO STRICT o FROM untracked_organization_operation WHERE id=op;
 SELECT * INTO STRICT c FROM untracked_review_confirmation WHERE id=o.confirmation_id;
 SELECT * INTO STRICT p FROM transfer_plan WHERE id=o.plan_id;
 SELECT * INTO STRICT s FROM transfer_step WHERE id=o.step_id;
 SELECT * INTO STRICT j FROM job WHERE id=o.job_id;
 m:=o.material->'confirmation'->'material';
 SELECT * INTO STRICT u FROM untracked_organization_unit WHERE operation_id=o.id AND unit_id=s.unit_id
  AND unit_key=m->'review'->'evidence'->'candidate'->'units'->0->>'unit_key';
 expected:=jsonb_build_object(
  'untracked_organization_id',o.id,'confirmation_id',o.confirmation_id,
  'scope_id',o.scope_id,'scope_version',(m->'scope'->>'version')::bigint,'file_id',o.file_id,'file_revision',(m->'file'->>'Revision')::bigint,
  'material_hash',o.material->'operation'->>'material_hash','media_type',m->'scope'->>'target_media_type',
  'media_id',u.media_id,'unit_id',u.unit_id,'unit_key',u.unit_key,'media_version',u.media_metadata_version,'unit_version',u.metadata_version,
  'parser_version',replace(m->'review'->'evidence'->>'parser_version','untracked-review.v1/',''),
  'source_root',m->'scope'->>'local_root','source_root_identity',m->'scope'->'source_identity',
  'source_rel',m->'file'->>'RelativePath','source_identity',m->'file'->'Identity',
  'root_id',(m->'scope'->>'target_root_id')::uuid,'root_path',m->'scope'->>'target_root_path',
  'root_generation',(m->'scope'->>'target_root_generation')::bigint,'root_device',(m->'scope'->'target_identity'->>'Device')::bigint,
  'root_inode',(m->'scope'->'target_identity'->>'Inode')::bigint,'baseline_scan_id',(m->'scope'->>'target_baseline_scan_id')::uuid,
  'marker_filename',m->'scope'->>'target_marker','target_rel',m->'input'->>'target_relative_path');
 IF c.owner_id<>o.owner_id OR c.scope_id<>o.scope_id OR c.file_id<>o.file_id OR c.receipt IS DISTINCT FROM o.material->'confirmation'
  OR o.receipt->'operation' IS DISTINCT FROM o.material->'operation' OR o.receipt->>'state' IS DISTINCT FROM 'queued' OR o.receipt->'execution_allowed' IS DISTINCT FROM 'false'::jsonb
  OR p.operation_id<>o.id OR p.root_id IS DISTINCT FROM (expected->>'root_id')::uuid OR NOT p.requires_approval
  OR p.strategy IS DISTINCT FROM m->'input'->>'action' OR p.strategy NOT IN ('hardlink','copy','reflink','move')
  OR p.root_path IS DISTINCT FROM expected->>'root_path' OR p.root_generation IS DISTINCT FROM (expected->>'root_generation')::bigint
  OR p.root_device IS DISTINCT FROM (expected->>'root_device')::bigint OR p.root_inode IS DISTINCT FROM (expected->>'root_inode')::bigint OR p.old_file_id IS NOT NULL
  OR p.created_at IS DISTINCT FROM (o.material->'operation'->>'created_at')::timestamptz
  OR s.plan_id<>p.id OR s.action<>p.strategy OR s.step_order<>0 OR s.created_at IS DISTINCT FROM p.created_at
  OR s.source_root IS DISTINCT FROM expected->>'source_root' OR s.source_rel IS DISTINCT FROM expected->>'source_rel'
  OR s.target_rel IS DISTINCT FROM expected->>'target_rel' OR s.source_path IS DISTINCT FROM rtrim(s.source_root,'/')||'/'||s.source_rel
  OR s.target_path IS DISTINCT FROM rtrim(p.root_path,'/')||'/'||s.target_rel
  OR s.source_size IS DISTINCT FROM (m->'file'->'Identity'->>'Size')::bigint OR s.source_mtime IS DISTINCT FROM (m->'file'->'Identity'->>'Mtime')::bigint
  OR s.source_device IS DISTINCT FROM (m->'file'->'Identity'->>'Device')::bigint OR s.source_inode IS DISTINCT FROM (m->'file'->'Identity'->>'Inode')::bigint
  OR s.target_evidence IS DISTINCT FROM expected OR jsonb_typeof(s.quality_profile) IS DISTINCT FROM 'object'
  OR j.kind<>'untracked_organization' OR j.max_attempts<>3 OR j.payload IS DISTINCT FROM jsonb_build_object('operation_id',o.id)
  OR (SELECT count(*) FROM transfer_step WHERE plan_id=p.id)<>1
  OR (SELECT count(*) FROM untracked_organization_unit WHERE operation_id=o.id)<>jsonb_array_length(m->'review'->'evidence'->'candidate'->'units') THEN
  RAISE EXCEPTION 'untracked organization frozen graph mismatch';
 END IF;
 IF EXISTS(
  SELECT 1 FROM untracked_organization_unit f LEFT JOIN media_unit mu ON mu.id=f.unit_id LEFT JOIN media md ON md.id=f.media_id
  LEFT JOIN media_identity mi ON mi.media_id=md.id AND mi.namespace=f.media_identity->>'namespace' AND mi.external_id=f.media_identity->>'external_id'
  LEFT JOIN media_unit_identity ei ON ei.unit_id=mu.id AND ei.namespace='tmdb.episode'
  WHERE f.operation_id=o.id AND (mu.id IS NULL OR md.id IS NULL OR mi.id IS NULL OR mu.retired OR mu.media_id<>f.media_id
   OR mu.metadata_version<>f.metadata_version OR md.metadata_version<>f.media_metadata_version OR mu.unit_key<>f.unit_key
   OR md.title IS DISTINCT FROM m->'review'->'evidence'->'candidate'->>'title'
   OR coalesce(md.original_title,'') IS DISTINCT FROM m->'review'->'evidence'->'candidate'->>'original_title'
   OR coalesce(md.year,0) IS DISTINCT FROM (m->'review'->'evidence'->'candidate'->>'year')::int
   OR md.media_type IS DISTINCT FROM m->'scope'->>'target_media_type'
   OR f.media_identity IS DISTINCT FROM jsonb_build_object('namespace',m->'review'->'evidence'->'candidate'->>'namespace','external_id',m->'review'->'evidence'->'candidate'->>'tmdb_id')
   OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(m->'review'->'evidence'->'candidate'->'units') x WHERE x=f.candidate_unit AND x->>'unit_key'=f.unit_key)
   OR mu.absolute_number IS NOT NULL OR mu.air_date IS DISTINCT FROM (f.candidate_unit->>'air_date')::timestamptz::date
   OR (md.media_type='movie' AND (mu.unit_key<>'movie' OR f.tmdb_episode_id<>0 OR mu.season_number IS NOT NULL OR mu.episode_number IS NOT NULL))
   OR (md.media_type='tv' AND (mu.season_number IS DISTINCT FROM (f.candidate_unit->>'season')::int OR mu.episode_number IS DISTINCT FROM (f.candidate_unit->>'episode')::int
    OR ei.external_id IS DISTINCT FROM f.tmdb_episode_id OR ei.tmdb_series_id IS DISTINCT FROM (m->'review'->'evidence'->'candidate'->>'tmdb_id')::bigint)))) THEN
  RAISE EXCEPTION 'untracked organization current metadata mismatch';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM app_user a WHERE a.id=o.owner_id AND a.is_active AND a.role='admin')
  OR EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated)
  OR NOT EXISTS(SELECT 1 FROM untracked_directory_scope scope WHERE scope.id=o.scope_id AND scope.owner_id=o.owner_id AND scope.enabled
   AND scope.version=(m->'scope'->>'version')::bigint AND scope.snapshot=m->'scope')
  OR NOT EXISTS(SELECT 1 FROM untracked_discovered_file f WHERE f.id=o.file_id AND f.scope_id=o.scope_id AND f.scope_version=(m->'file'->>'ScopeVersion')::bigint
   AND f.revision=(m->'file'->>'Revision')::bigint AND f.relative_path=s.source_rel AND f.device=s.source_device AND f.inode=s.source_inode AND f.size=s.source_size AND f.mtime=s.source_mtime)
  OR NOT EXISTS(SELECT 1 FROM library_root r WHERE r.id=p.root_id AND r.path=p.root_path AND r.generation=p.root_generation AND r.health='online' AND r.baseline_completed
   AND r.media_type=expected->>'media_type' AND r.marker_filename=expected->>'marker_filename'
   AND (SELECT f.scan_run_id FROM scan_finalization f JOIN scan_run run ON run.id=f.scan_run_id WHERE f.root_id=r.id AND run.generation=r.generation AND run.state='succeeded' AND f.completed_at IS NOT NULL ORDER BY run.created_at,run.id LIMIT 1)=(expected->>'baseline_scan_id')::uuid
   AND EXISTS(SELECT 1 FROM scan_finalization f WHERE f.scan_run_id=(expected->>'baseline_scan_id')::uuid AND f.root_id=r.id AND f.root_device=p.root_device AND f.root_inode=p.root_inode AND f.expected_path=r.path AND f.expected_media_type=r.media_type AND f.expected_marker_filename=r.marker_filename))
  OR NOT EXISTS(SELECT 1 FROM download_client dc WHERE dc.id=(m->'scope'->>'client_id')::uuid AND dc.updated_at=(m->'scope'->>'client_version')::timestamptz
   AND dc.remote_path_map->>(m->'scope'->>'remote_prefix')=s.source_root
   AND encode(sha256(convert_to(jsonb_build_object('id',dc.id,'type',dc.client_type,'version',dc.updated_at AT TIME ZONE 'UTC','url',dc.url,'username',dc.username,'password',dc.password_encrypted,'mapping',dc.remote_path_map)::text,'UTF8')),'hex')=m->'scope'->>'client_stamp') THEN
  RAISE EXCEPTION 'untracked organization current authority mismatch';
 END IF;
 SELECT * INTO permit FROM untracked_organization_permit WHERE operation_id=o.id;
 IF FOUND THEN
  SELECT e.content INTO STRICT content FROM transfer_content_evidence e WHERE e.step_id=s.id AND e.inherited_from IS NULL;
  IF jsonb_typeof(content->'SHA256') IS DISTINCT FROM 'array' OR jsonb_array_length(content->'SHA256')<>32
   OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(content->'SHA256') b(value) WHERE value::int NOT BETWEEN 0 AND 255)
   OR content->'File' IS DISTINCT FROM m->'file'->'Identity' THEN RAISE EXCEPTION 'untracked organization content evidence mismatch'; END IF;
  SELECT decode(string_agg(lpad(to_hex(value::int),2,'0') ,'' ORDER BY ord),'hex') INTO digest FROM jsonb_array_elements_text(content->'SHA256') WITH ORDINALITY a(value,ord);
  IF octet_length(digest)<>32 OR digest<>permit.content_sha256 OR permit.content_size<>s.source_size OR permit.job_id<>j.id
   OR (TG_TABLE_NAME='untracked_organization_permit' AND (permit.job_owner IS DISTINCT FROM j.lease_owner OR permit.job_token<>j.lease_token))
   OR permit.execution_owner IS DISTINCT FROM p.execution_owner OR permit.execution_token<>p.execution_token
   OR permit.issued_at<p.created_at OR permit.issued_at>clock_timestamp() OR j.state NOT IN ('running','succeeded')
   OR j.attempts<1 OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
   OR (TG_TABLE_NAME='untracked_organization_permit' AND (p.lease_until IS NULL OR p.lease_until<=clock_timestamp())) THEN
   RAISE EXCEPTION 'untracked organization permanent permit mismatch';
  END IF;
 END IF;
 IF TG_TABLE_NAME='untracked_organization_observation' THEN
  IF permit.operation_id IS NULL OR NEW.job_id<>j.id OR NEW.job_owner IS DISTINCT FROM j.lease_owner OR NEW.job_token<>j.lease_token OR NEW.recorded_at<permit.issued_at OR NEW.recorded_at>clock_timestamp() THEN RAISE EXCEPTION 'untracked organization observation mismatch'; END IF;
 END IF;
 SELECT * INTO done FROM untracked_organization_completion WHERE operation_id=o.id;
 IF p.state='completed' OR s.state='completed' OR j.state='succeeded' OR FOUND THEN
  IF done.operation_id IS NULL OR permit.operation_id IS NULL OR p.state<>'completed' OR s.state<>'completed' OR j.state<>'succeeded'
   OR j.finished_at IS NULL OR j.error IS NOT NULL OR s.error IS NOT NULL OR s.target_file_id IS DISTINCT FROM done.file_id OR done.step_id<>s.id THEN
   RAISE EXCEPTION 'untracked organization incomplete completion graph';
  END IF;
  SELECT * INTO STRICT observation FROM untracked_organization_observation WHERE id=done.observation_id AND operation_id=o.id;
  IF observation.result IS DISTINCT FROM done.result OR observation.job_id<>j.id OR observation.job_owner IS DISTINCT FROM j.lease_owner OR observation.job_token<>j.lease_token
   OR observation.recorded_at<permit.issued_at OR done.recorded_at<observation.recorded_at OR done.recorded_at>clock_timestamp()
   OR done.result->'Target'->'SHA256' IS DISTINCT FROM content->'SHA256'
   OR (done.result->'Target'->'File'->>'Size')::bigint IS DISTINCT FROM permit.content_size
   OR coalesce((done.result->'Target'->'File'->>'Device')::bigint,-1)<0
   OR coalesce((done.result->'Target'->'File'->>'Inode')::bigint,0)<=0
   OR done.result->'SourceRemoved' IS DISTINCT FROM to_jsonb(s.action='move')
   OR (s.action='hardlink' AND done.result->'Target'->'File' IS DISTINCT FROM content->'File')
   OR NOT EXISTS(SELECT 1 FROM library_file f WHERE f.id=done.file_id AND f.root_id=p.root_id AND f.rel_path=s.target_rel AND f.retired_at IS NULL
    AND f.size_bytes=permit.content_size AND f.mtime_ns=(done.result->'Target'->'File'->>'Mtime')::bigint)
   OR EXISTS(SELECT 1 FROM untracked_organization_unit f WHERE f.operation_id=o.id AND NOT EXISTS(SELECT 1 FROM library_file_unit fu WHERE fu.file_id=done.file_id AND fu.unit_id=f.unit_id AND fu.retired_at IS NULL))
   OR EXISTS(SELECT 1 FROM library_file_unit fu WHERE fu.file_id=done.file_id AND fu.retired_at IS NULL AND NOT EXISTS(SELECT 1 FROM untracked_organization_unit f WHERE f.operation_id=o.id AND f.unit_id=fu.unit_id))
   OR EXISTS(SELECT 1 FROM untracked_organization_unit f WHERE f.operation_id=o.id AND (NOT EXISTS(SELECT 1 FROM unit_fact fact WHERE fact.unit_id=f.unit_id AND fact.root_id=p.root_id AND fact.source_file_id=done.file_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND fact.quality_profile=s.quality_profile AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp()))
    OR EXISTS(SELECT 1 FROM unit_fact fact WHERE fact.unit_id=f.unit_id AND fact.root_id=p.root_id AND fact.source_file_id IS DISTINCT FROM done.file_id AND fact.stage='imported' AND fact.revoked_at IS NULL AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp())))) THEN
   RAISE EXCEPTION 'untracked organization completion proof mismatch';
  END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER untracked_organization_plan_deferred AFTER INSERT OR UPDATE ON transfer_plan DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_deferred_guard();
CREATE CONSTRAINT TRIGGER untracked_organization_step_deferred AFTER INSERT OR UPDATE ON transfer_step DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_deferred_guard();
CREATE CONSTRAINT TRIGGER untracked_organization_operation_deferred AFTER INSERT ON untracked_organization_operation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_deferred_guard();
CREATE CONSTRAINT TRIGGER untracked_organization_permit_deferred AFTER INSERT ON untracked_organization_permit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_deferred_guard();
CREATE CONSTRAINT TRIGGER untracked_organization_observation_deferred AFTER INSERT ON untracked_organization_observation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_deferred_guard();
CREATE CONSTRAINT TRIGGER untracked_organization_completion_deferred AFTER INSERT ON untracked_organization_completion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_organization_deferred_guard();

REVOKE ALL ON untracked_organization_operation,untracked_organization_unit,untracked_organization_permit,untracked_organization_observation,untracked_organization_completion FROM PUBLIC;

-- External steps have their own complete deferred authority ledger above.
-- Keep the legacy download guard body and automatic-TV dispatch unchanged.
DROP TRIGGER transfer_target_evidence_guard ON transfer_step;
CREATE TRIGGER transfer_target_evidence_guard BEFORE UPDATE ON transfer_step FOR EACH ROW
 WHEN (NOT COALESCE(NEW.target_evidence ? 'automatic_tv_operation_id',false)
 AND NOT COALESCE(NEW.target_evidence ? 'untracked_organization_id',false)) EXECUTE FUNCTION pulse_transfer_target_evidence_guard();

-- +goose Down
LOCK TABLE untracked_organization_operation,untracked_organization_unit,untracked_organization_permit,untracked_organization_observation,untracked_organization_completion,transfer_plan,transfer_step,job IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM untracked_organization_operation) OR EXISTS(SELECT 1 FROM untracked_organization_unit)
  OR EXISTS(SELECT 1 FROM untracked_organization_permit) OR EXISTS(SELECT 1 FROM untracked_organization_observation)
  OR EXISTS(SELECT 1 FROM untracked_organization_completion) OR EXISTS(SELECT 1 FROM job WHERE kind='untracked_organization')
  OR EXISTS(SELECT 1 FROM transfer_step WHERE target_evidence ? 'untracked_organization_id') THEN
  RAISE EXCEPTION 'cannot drop untracked organization history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER transfer_target_evidence_guard ON transfer_step;
CREATE TRIGGER transfer_target_evidence_guard BEFORE UPDATE ON transfer_step FOR EACH ROW
 WHEN (NOT COALESCE(NEW.target_evidence ? 'automatic_tv_operation_id',false)) EXECUTE FUNCTION pulse_transfer_target_evidence_guard();
DROP TRIGGER untracked_organization_plan_frozen ON transfer_plan;
DROP TRIGGER untracked_organization_step_frozen ON transfer_step;
DROP TRIGGER untracked_organization_plan_truncate ON transfer_plan;
DROP TRIGGER untracked_organization_step_truncate ON transfer_step;
DROP TRIGGER untracked_organization_plan_deferred ON transfer_plan;
DROP TRIGGER untracked_organization_step_deferred ON transfer_step;
DROP TRIGGER untracked_organization_operation_deferred ON untracked_organization_operation;
DROP TRIGGER untracked_organization_permit_deferred ON untracked_organization_permit;
DROP TRIGGER untracked_organization_observation_deferred ON untracked_organization_observation;
DROP TRIGGER untracked_organization_completion_deferred ON untracked_organization_completion;
DROP FUNCTION pulse_untracked_organization_deferred_guard();
DROP FUNCTION pulse_untracked_organization_transfer_frozen();
DROP TABLE untracked_organization_completion;
DROP TABLE untracked_organization_observation;
DROP TABLE untracked_organization_permit;
DROP TABLE untracked_organization_unit;
DROP TABLE untracked_organization_operation;
DROP FUNCTION pulse_untracked_organization_consistency();
DROP FUNCTION pulse_untracked_organization_history_guard();
