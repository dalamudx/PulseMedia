-- +goose Up
-- Only an explicitly confirmed subscription mode permits continued replacement.
-- Keep the original basis JSON shape so pending cutoff work remains valid.
ALTER TABLE subscription DROP CONSTRAINT subscription_upgrade_mode_check;
ALTER TABLE subscription ADD CONSTRAINT subscription_upgrade_mode_check
 CHECK(upgrade_mode IN ('disabled','stop_at_cutoff','replace_if_better'));
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_upgrade_organization_basis(op UUID,obs UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT jsonb_build_object(
 'version','movie-upgrade.organization.v1','operation_id',a.operation_id,
 'observation_id',o.observation_id,'task_id',p.task_id,'actor_id',a.actor_id,
 'subscription_id',a.subscription_id,'root_id',a.root_id,'unit_id',a.unit_id,
 'old_file_id',a.old_file_id,'old_fact_id',a.old_fact_id,
 'original_sha256',pulse_upgrade_tracking_original_digest(op),
 'protected_sha256',pulse_upgrade_observation_protected_digest(op),
 'tracking_sha256',sha256(convert_to(jsonb_build_array(to_jsonb(o),to_jsonb(permit),to_jsonb(r))::text,'UTF8')),
 'actor_sha256',sha256(convert_to((to_jsonb(actor)-'password_hash')::text,'UTF8')),
 'tracking_ledger_sha256',sha256(convert_to(jsonb_build_object(
   'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM audit_log x WHERE action='upgrade.tracking-result' AND payload->>'observation_id'=obs::text),
   'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM outbox_event x WHERE event_type='upgrade.tracking-result' AND aggregate_id=obs::text),
   'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM idempotency_record x WHERE idempotency_key LIKE 'upgrade.tracking-result:%' AND resource_id=obs::text))::text,'UTF8')),
 'policy_id',pol.id,'policy_revision',pol.current_revision,'rules',rev.rules,
 'old_profile',fact.quality_profile,'candidate_profile',a.snapshot->'target'->'Candidate'->'Profile',
 'candidate_title',a.snapshot->'target'->'Candidate'->>'Title','proof',p.evidence->'proof',
 'save_path',p.evidence->>'save_path','root_path',root.path,
 'media_sha256',sha256(convert_to(to_jsonb(m)::text,'UTF8')),
 'identities_sha256',sha256(convert_to((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.namespace) FROM media_identity i WHERE i.media_id=m.id)::text,'UTF8')),
 'skip',w.is_skipped)
 FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id)
 JOIN app_user actor ON actor.id=a.actor_id AND actor.is_active
 JOIN movie_upgrade_tracking_observation o ON o.operation_id=a.operation_id AND o.observation_id=obs
 JOIN movie_upgrade_tracking_permit permit USING(observation_id)
 JOIN movie_upgrade_tracking_result r USING(observation_id)
 JOIN subscription s ON s.id=a.subscription_id JOIN policy pol ON pol.id=s.quality_policy_id
 JOIN policy_revision rev ON rev.policy_id=pol.id AND rev.revision=pol.current_revision
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id
 JOIN media m ON m.id=u.media_id JOIN library_file f ON f.id=a.old_file_id
 JOIN library_file_unit fu ON fu.file_id=f.id AND fu.unit_id=u.id AND fu.retired_at IS NULL
 JOIN unit_fact fact ON fact.id=a.old_fact_id
 JOIN wanted_item w ON w.subscription_id=s.id AND w.unit_id=u.id
 WHERE a.operation_id=op AND r.outcome='completed' AND r.disposition='organization_review_required'
 AND o.task_id=p.task_id AND o.actor_id=a.actor_id AND o.original_sha256=pulse_upgrade_tracking_original_digest(op)
 AND r.observation_sha256=sha256(convert_to(to_jsonb(o)::text,'UTF8'))
 AND r.permit_sha256=sha256(convert_to(to_jsonb(permit)::text,'UTF8'))
 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=obs)
 AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND s.upgrade_mode=a.snapshot->>'mode' AND s.intent_version=a.intent_version
 AND s.root_id=a.root_id AND s.media_id=m.id AND s.downloader_id=a.client_id
 AND pol.id=a.policy_id AND pol.current_revision=a.policy_revision AND s.quality_policy_revision=pol.current_revision
 AND root.health='online' AND root.baseline_completed AND root.generation=a.root_generation AND root.media_type='movie'
 AND m.media_type='movie' AND m.metadata_version=(p.evidence->'proof'->'target'->>'MediaVersion')::bigint
 AND COALESCE(NULLIF(m.original_title,''),m.title)=p.evidence->'proof'->'target'->>'OriginalTitle' AND m.year=(p.evidence->'proof'->'target'->>'Year')::integer
 AND (SELECT jsonb_agg(jsonb_build_object('namespace',i.namespace,'external_id',i.external_id) ORDER BY i.namespace) FROM media_identity i WHERE i.media_id=m.id)
 = (SELECT jsonb_agg(x ORDER BY x->>'namespace') FROM jsonb_array_elements(p.evidence->'proof'->'target'->'Identities') x)
 AND u.unit_key='movie' AND NOT u.retired AND u.metadata_version=(p.evidence->'proof'->'target'->>'UnitVersion')::bigint
 AND (p.evidence->'proof'->'target'->>'UnitID')::uuid=u.id
 AND (p.evidence->'proof'->'target'->>'MediaID')::uuid=m.id
 AND (SELECT count(*) FROM media_unit x WHERE x.media_id=m.id AND NOT x.retired)=1
 AND f.root_id=root.id AND f.retired_at IS NULL
 AND (SELECT count(*) FROM library_file_unit x WHERE x.file_id=f.id AND x.retired_at IS NULL)=1
 AND (SELECT count(*) FROM library_file_unit x JOIN library_file y ON y.id=x.file_id WHERE x.unit_id=u.id AND x.retired_at IS NULL AND y.root_id=root.id AND y.retired_at IS NULL)=1
 AND fact.unit_id=u.id AND fact.root_id=root.id AND fact.source_file_id=f.id AND fact.stage='imported' AND fact.revoked_at IS NULL
 AND (SELECT count(*) FROM unit_fact x WHERE x.unit_id=u.id AND x.root_id=root.id AND x.source_file_id=f.id AND x.stage='imported' AND x.revoked_at IS NULL)=1
 AND NOT w.is_skipped AND (SELECT count(*) FROM wanted_item x WHERE x.subscription_id=s.id)=1
 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b WHERE b.operation_id=op AND b.prior_observation_id IS NOT DISTINCT FROM o.prior_observation_id
   AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job_claim c JOIN job j ON j.id=c.job_id
    WHERE c.job_id=b.job_id AND c.observation_id=obs AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(obs)))
$$;
CREATE OR REPLACE FUNCTION pulse_upgrade_retirement_valid(op UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE(actor.is_active AND actor.role='admin' AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND s.upgrade_mode=a.snapshot->>'mode'
 AND s.intent_version=a.intent_version AND s.root_id=a.root_id AND s.quality_policy_id=a.policy_id AND pol.current_revision=a.policy_revision
 AND root.generation=a.root_generation AND root.health='online' AND root.baseline_completed AND NOT u.retired
 AND oldfile.retired_at IS NULL AND oldfile.root_id=a.root_id
 AND to_jsonb(oldfile)=(SELECT x FROM jsonb_array_elements(i.baseline->'files') x WHERE x->>'id'=a.old_file_id::text)
 AND to_jsonb(oldfact)=(SELECT x FROM jsonb_array_elements(i.baseline->'facts') x WHERE x->>'id'=a.old_fact_id::text)
 AND oldfact.revoked_at IS NULL AND (oldfact.expires_at IS NULL OR oldfact.expires_at>clock_timestamp())
 AND newfact.revoked_at IS NULL AND (newfact.expires_at IS NULL OR newfact.expires_at>clock_timestamp())
 AND to_jsonb(newfile)=i.file_snapshot AND to_jsonb(newfact)=i.fact_snapshot
 AND (SELECT count(*) FROM library_file_unit WHERE file_id=a.old_file_id AND retired_at IS NULL)=1
 AND EXISTS(SELECT 1 FROM library_file_unit WHERE file_id=a.old_file_id AND retired_at IS NULL AND unit_id=a.unit_id)
 AND (SELECT count(*) FROM library_file_unit WHERE file_id=i.file_id)=1
 AND EXISTS(SELECT 1 FROM library_file_unit x WHERE file_id=i.file_id AND to_jsonb(x)=i.mapping_snapshot)
 AND NOT EXISTS(SELECT 1 FROM unit_fact x WHERE x.source_file_id IN(a.old_file_id,i.file_id) AND x.unit_id<>a.unit_id AND x.revoked_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM wanted_item w WHERE w.subscription_id=s.id AND (w.is_skipped OR w.user_skipped OR w.scope_excluded))
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=op)=i.baseline->'lanes'
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=op)=i.baseline->'reservations'
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=op)=i.baseline->'requirements',false)
 FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id)
 JOIN app_user actor ON actor.id=a.actor_id JOIN subscription s ON s.id=a.subscription_id JOIN policy pol ON pol.id=s.quality_policy_id
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id
 JOIN library_file oldfile ON oldfile.id=a.old_file_id JOIN library_file newfile ON newfile.id=i.file_id
 JOIN unit_fact oldfact ON oldfact.id=a.old_fact_id JOIN unit_fact newfact ON newfact.id=i.fact_id WHERE a.operation_id=op
$$;
-- +goose StatementEnd

-- +goose Down
LOCK TABLE subscription,movie_upgrade_authority,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM subscription WHERE upgrade_mode='replace_if_better')
 OR EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE snapshot->>'mode'='replace_if_better')
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='subscription.upgrade_mode' AND payload->>'mode'='replace_if_better')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='subscription.upgrade_mode' AND payload->>'mode'='replace_if_better')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.mode:%' AND response_body->>'mode'='replace_if_better') THEN
  RAISE EXCEPTION 'replacement mode history prevents downgrade';
 END IF;
END $$;
CREATE OR REPLACE FUNCTION pulse_upgrade_organization_basis(op UUID,obs UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
 SELECT jsonb_build_object(
 'version','movie-upgrade.organization.v1','operation_id',a.operation_id,
 'observation_id',o.observation_id,'task_id',p.task_id,'actor_id',a.actor_id,
 'subscription_id',a.subscription_id,'root_id',a.root_id,'unit_id',a.unit_id,
 'old_file_id',a.old_file_id,'old_fact_id',a.old_fact_id,
 'original_sha256',pulse_upgrade_tracking_original_digest(op),
 'protected_sha256',pulse_upgrade_observation_protected_digest(op),
 'tracking_sha256',sha256(convert_to(jsonb_build_array(to_jsonb(o),to_jsonb(permit),to_jsonb(r))::text,'UTF8')),
 'actor_sha256',sha256(convert_to((to_jsonb(actor)-'password_hash')::text,'UTF8')),
 'tracking_ledger_sha256',sha256(convert_to(jsonb_build_object(
   'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM audit_log x WHERE action='upgrade.tracking-result' AND payload->>'observation_id'=obs::text),
   'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM outbox_event x WHERE event_type='upgrade.tracking-result' AND aggregate_id=obs::text),
   'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM idempotency_record x WHERE idempotency_key LIKE 'upgrade.tracking-result:%' AND resource_id=obs::text))::text,'UTF8')),
 'policy_id',pol.id,'policy_revision',pol.current_revision,'rules',rev.rules,
 'old_profile',fact.quality_profile,'candidate_profile',a.snapshot->'target'->'Candidate'->'Profile',
 'candidate_title',a.snapshot->'target'->'Candidate'->>'Title','proof',p.evidence->'proof',
 'save_path',p.evidence->>'save_path','root_path',root.path,
 'media_sha256',sha256(convert_to(to_jsonb(m)::text,'UTF8')),
 'identities_sha256',sha256(convert_to((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.namespace) FROM media_identity i WHERE i.media_id=m.id)::text,'UTF8')),
 'skip',w.is_skipped)
 FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id)
 JOIN app_user actor ON actor.id=a.actor_id AND actor.is_active
 JOIN movie_upgrade_tracking_observation o ON o.operation_id=a.operation_id AND o.observation_id=obs
 JOIN movie_upgrade_tracking_permit permit USING(observation_id)
 JOIN movie_upgrade_tracking_result r USING(observation_id)
 JOIN subscription s ON s.id=a.subscription_id JOIN policy pol ON pol.id=s.quality_policy_id
 JOIN policy_revision rev ON rev.policy_id=pol.id AND rev.revision=pol.current_revision
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id
 JOIN media m ON m.id=u.media_id JOIN library_file f ON f.id=a.old_file_id
 JOIN library_file_unit fu ON fu.file_id=f.id AND fu.unit_id=u.id AND fu.retired_at IS NULL
 JOIN unit_fact fact ON fact.id=a.old_fact_id
 JOIN wanted_item w ON w.subscription_id=s.id AND w.unit_id=u.id
 WHERE a.operation_id=op AND r.outcome='completed' AND r.disposition='organization_review_required'
 AND o.task_id=p.task_id AND o.actor_id=a.actor_id AND o.original_sha256=pulse_upgrade_tracking_original_digest(op)
 AND r.observation_sha256=sha256(convert_to(to_jsonb(o)::text,'UTF8'))
 AND r.permit_sha256=sha256(convert_to(to_jsonb(permit)::text,'UTF8'))
 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=obs)
 AND s.state='active' AND s.upgrade_mode='stop_at_cutoff' AND s.intent_version=a.intent_version
 AND s.root_id=a.root_id AND s.media_id=m.id AND s.downloader_id=a.client_id
 AND pol.id=a.policy_id AND pol.current_revision=a.policy_revision AND s.quality_policy_revision=pol.current_revision
 AND root.health='online' AND root.baseline_completed AND root.generation=a.root_generation AND root.media_type='movie'
 AND m.media_type='movie' AND m.metadata_version=(p.evidence->'proof'->'target'->>'MediaVersion')::bigint
 AND COALESCE(NULLIF(m.original_title,''),m.title)=p.evidence->'proof'->'target'->>'OriginalTitle' AND m.year=(p.evidence->'proof'->'target'->>'Year')::integer
 AND (SELECT jsonb_agg(jsonb_build_object('namespace',i.namespace,'external_id',i.external_id) ORDER BY i.namespace) FROM media_identity i WHERE i.media_id=m.id)
 = (SELECT jsonb_agg(x ORDER BY x->>'namespace') FROM jsonb_array_elements(p.evidence->'proof'->'target'->'Identities') x)
 AND u.unit_key='movie' AND NOT u.retired AND u.metadata_version=(p.evidence->'proof'->'target'->>'UnitVersion')::bigint
 AND (p.evidence->'proof'->'target'->>'UnitID')::uuid=u.id
 AND (p.evidence->'proof'->'target'->>'MediaID')::uuid=m.id
 AND (SELECT count(*) FROM media_unit x WHERE x.media_id=m.id AND NOT x.retired)=1
 AND f.root_id=root.id AND f.retired_at IS NULL
 AND (SELECT count(*) FROM library_file_unit x WHERE x.file_id=f.id AND x.retired_at IS NULL)=1
 AND (SELECT count(*) FROM library_file_unit x JOIN library_file y ON y.id=x.file_id WHERE x.unit_id=u.id AND x.retired_at IS NULL AND y.root_id=root.id AND y.retired_at IS NULL)=1
 AND fact.unit_id=u.id AND fact.root_id=root.id AND fact.source_file_id=f.id AND fact.stage='imported' AND fact.revoked_at IS NULL
 AND (SELECT count(*) FROM unit_fact x WHERE x.unit_id=u.id AND x.root_id=root.id AND x.source_file_id=f.id AND x.stage='imported' AND x.revoked_at IS NULL)=1
 AND NOT w.is_skipped AND (SELECT count(*) FROM wanted_item x WHERE x.subscription_id=s.id)=1
 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b WHERE b.operation_id=op AND b.prior_observation_id IS NOT DISTINCT FROM o.prior_observation_id
   AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job_claim c JOIN job j ON j.id=c.job_id
    WHERE c.job_id=b.job_id AND c.observation_id=obs AND to_jsonb(j)=pulse_upgrade_tracking_terminal_job(obs)))
$$;
CREATE OR REPLACE FUNCTION pulse_upgrade_retirement_valid(op UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT COALESCE(actor.is_active AND actor.role='admin' AND s.state='active' AND s.upgrade_mode='stop_at_cutoff'
 AND s.intent_version=a.intent_version AND s.root_id=a.root_id AND s.quality_policy_id=a.policy_id AND pol.current_revision=a.policy_revision
 AND root.generation=a.root_generation AND root.health='online' AND root.baseline_completed AND NOT u.retired
 AND oldfile.retired_at IS NULL AND oldfile.root_id=a.root_id
 AND to_jsonb(oldfile)=(SELECT x FROM jsonb_array_elements(i.baseline->'files') x WHERE x->>'id'=a.old_file_id::text)
 AND to_jsonb(oldfact)=(SELECT x FROM jsonb_array_elements(i.baseline->'facts') x WHERE x->>'id'=a.old_fact_id::text)
 AND oldfact.revoked_at IS NULL AND (oldfact.expires_at IS NULL OR oldfact.expires_at>clock_timestamp())
 AND newfact.revoked_at IS NULL AND (newfact.expires_at IS NULL OR newfact.expires_at>clock_timestamp())
 AND to_jsonb(newfile)=i.file_snapshot AND to_jsonb(newfact)=i.fact_snapshot
 AND (SELECT count(*) FROM library_file_unit WHERE file_id=a.old_file_id AND retired_at IS NULL)=1
 AND EXISTS(SELECT 1 FROM library_file_unit WHERE file_id=a.old_file_id AND retired_at IS NULL AND unit_id=a.unit_id)
 AND (SELECT count(*) FROM library_file_unit WHERE file_id=i.file_id)=1
 AND EXISTS(SELECT 1 FROM library_file_unit x WHERE file_id=i.file_id AND to_jsonb(x)=i.mapping_snapshot)
 AND NOT EXISTS(SELECT 1 FROM unit_fact x WHERE x.source_file_id IN(a.old_file_id,i.file_id) AND x.unit_id<>a.unit_id AND x.revoked_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM wanted_item w WHERE w.subscription_id=s.id AND (w.is_skipped OR w.user_skipped OR w.scope_excluded))
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=op)=i.baseline->'lanes'
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=op)=i.baseline->'reservations'
 AND (SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=op)=i.baseline->'requirements',false)
 FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id)
 JOIN app_user actor ON actor.id=a.actor_id JOIN subscription s ON s.id=a.subscription_id JOIN policy pol ON pol.id=s.quality_policy_id
 JOIN library_root root ON root.id=a.root_id JOIN media_unit u ON u.id=a.unit_id
 JOIN library_file oldfile ON oldfile.id=a.old_file_id JOIN library_file newfile ON newfile.id=i.file_id
 JOIN unit_fact oldfact ON oldfact.id=a.old_fact_id JOIN unit_fact newfact ON newfact.id=i.fact_id WHERE a.operation_id=op
$$;
-- +goose StatementEnd
ALTER TABLE subscription DROP CONSTRAINT subscription_upgrade_mode_check;
ALTER TABLE subscription ADD CONSTRAINT subscription_upgrade_mode_check
 CHECK(upgrade_mode IN ('disabled','stop_at_cutoff'));
