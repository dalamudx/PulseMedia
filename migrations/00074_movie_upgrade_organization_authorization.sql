-- +goose Up
-- Completed tracking is not an execution capability. This table freezes a new
-- explicit organization command without creating a plan, step, job or task.
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_organization_basis(op UUID,obs UUID) RETURNS JSONB LANGUAGE SQL STABLE AS $$
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
-- +goose StatementEnd
CREATE TABLE movie_upgrade_organization_authorization (
 id UUID PRIMARY KEY CHECK(id<>'00000000-0000-0000-0000-000000000000'),
 operation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_preparation(operation_id),
 observation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_tracking_result(observation_id),
 task_id UUID NOT NULL REFERENCES download_task(id), actor_id UUID NOT NULL REFERENCES app_user(id),
 subscription_id UUID NOT NULL REFERENCES subscription(id), root_id UUID NOT NULL REFERENCES library_root(id),
 unit_id UUID NOT NULL REFERENCES media_unit(id), old_file_id UUID NOT NULL REFERENCES library_file(id), old_fact_id UUID NOT NULL REFERENCES unit_fact(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 32 AND 180 AND request_key LIKE 'upgrade.organization-authorize:%'),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 input_sha256 BYTEA NOT NULL CHECK(octet_length(input_sha256)=32),
 authorized_at TIMESTAMPTZ NOT NULL CHECK(isfinite(authorized_at)),
 confirmed BOOLEAN NOT NULL CHECK(confirmed),
 status TEXT NOT NULL CHECK(status='organization_authorized_not_executable'),
 execution_allowed BOOLEAN NOT NULL CHECK(NOT execution_allowed),
 UNIQUE(actor_id,request_key)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_organization_authorization_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE basis JSONB;
BEGIN
 basis:=pulse_upgrade_organization_basis(NEW.operation_id,NEW.observation_id);
 IF basis IS NULL OR NEW.snapshot IS DISTINCT FROM basis
 OR NEW.input_sha256 IS DISTINCT FROM sha256(convert_to(basis::text,'UTF8'))
 OR NEW.task_id IS DISTINCT FROM (basis->>'task_id')::uuid OR NEW.actor_id IS DISTINCT FROM (basis->>'actor_id')::uuid
 OR NEW.subscription_id IS DISTINCT FROM (basis->>'subscription_id')::uuid OR NEW.root_id IS DISTINCT FROM (basis->>'root_id')::uuid
 OR NEW.unit_id IS DISTINCT FROM (basis->>'unit_id')::uuid OR NEW.old_file_id IS DISTINCT FROM (basis->>'old_file_id')::uuid
 OR NEW.old_fact_id IS DISTINCT FROM (basis->>'old_fact_id')::uuid OR NEW.authorized_at>clock_timestamp()
 THEN RAISE EXCEPTION 'upgrade organization authority binding mismatch' USING ERRCODE='23514'; END IF;
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_organization_authorization_binding AFTER INSERT ON movie_upgrade_organization_authorization DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_authorization_guard();
CREATE TRIGGER movie_upgrade_organization_authorization_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_authorization FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
REVOKE ALL ON movie_upgrade_organization_authorization FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_organization_authorization,movie_upgrade_tracking_observation,movie_upgrade_tracking_result,movie_upgrade_tracking_permit,
 movie_upgrade_tracking_job,movie_upgrade_tracking_job_claim,job,movie_upgrade_authority,movie_upgrade_preparation,
 movie_upgrade_send_authorization,movie_upgrade_send_dispatch,movie_upgrade_send_result,download_task,
 subscription,policy,policy_revision,library_root,library_file,library_file_unit,unit_fact,media_unit,media,media_identity,wanted_item,
 acquisition_reservation,unit_reservation,download_requirement,download_client,app_user,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_organization_authorization)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.organization-authorize')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='upgrade.organization-authorize')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.organization-authorize:%')
 THEN RAISE EXCEPTION 'upgrade organization history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE movie_upgrade_organization_authorization;
DROP FUNCTION pulse_upgrade_organization_authorization_guard();
DROP FUNCTION pulse_upgrade_organization_basis(UUID,UUID);
