-- +goose Up
CREATE TABLE movie_upgrade_organization_preparation (
 id UUID PRIMARY KEY CHECK(id<>'00000000-0000-0000-0000-000000000000'),
 operation_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_preparation(operation_id),
 authorization_id UUID NOT NULL UNIQUE REFERENCES movie_upgrade_organization_authorization(id),
 task_id UUID NOT NULL REFERENCES download_task(id),actor_id UUID NOT NULL REFERENCES app_user(id),
 plan_id UUID NOT NULL UNIQUE REFERENCES transfer_plan(id),step_id UUID NOT NULL UNIQUE REFERENCES transfer_step(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 30 AND 180 AND request_key LIKE 'upgrade.organization-prepare:%'),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 plan_snapshot JSONB NOT NULL CHECK(jsonb_typeof(plan_snapshot)='object'),
 step_snapshot JSONB NOT NULL CHECK(jsonb_typeof(step_snapshot)='object'),
 observed_at TIMESTAMPTZ NOT NULL CHECK(isfinite(observed_at)),
 prepared_at TIMESTAMPTZ NOT NULL CHECK(isfinite(prepared_at) AND prepared_at>=observed_at AND prepared_at<observed_at+interval '30 seconds'),
 status TEXT NOT NULL CHECK(status='organization_prepared_not_executable'),
 execution_allowed BOOLEAN NOT NULL CHECK(NOT execution_allowed),
 UNIQUE(actor_id,request_key)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_assert_upgrade_organization_preparation(op UUID) RETURNS void LANGUAGE plpgsql AS $$
DECLARE p movie_upgrade_organization_preparation%ROWTYPE; a movie_upgrade_organization_authorization%ROWTYPE; plan JSONB; step JSONB; physical JSONB; oldfile library_file%ROWTYPE;
BEGIN
 SELECT * INTO p FROM movie_upgrade_organization_preparation WHERE operation_id=op;
 IF NOT FOUND THEN
  IF EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=op) AND EXISTS(SELECT 1 FROM transfer_plan WHERE operation_id=op) THEN
   RAISE EXCEPTION 'upgrade transfer plan has no frozen preparation' USING ERRCODE='23514';
  END IF;
  RETURN;
 END IF;
 SELECT * INTO a FROM movie_upgrade_organization_authorization WHERE id=p.authorization_id;
 SELECT * INTO oldfile FROM library_file WHERE id=a.old_file_id;
 SELECT to_jsonb(x) INTO plan FROM transfer_plan x WHERE id=p.plan_id;
 SELECT to_jsonb(x) INTO step FROM transfer_step x WHERE id=p.step_id;
 physical:=p.snapshot->'physical';
 IF a.id IS NULL OR a.operation_id IS DISTINCT FROM op OR a.task_id IS DISTINCT FROM p.task_id OR a.actor_id IS DISTINCT FROM p.actor_id
 OR p.snapshot->'authorization' IS DISTINCT FROM to_jsonb(a) OR p.snapshot->'basis' IS DISTINCT FROM a.snapshot
 OR plan IS NULL OR step IS NULL OR plan IS DISTINCT FROM p.plan_snapshot OR step IS DISTINCT FROM p.step_snapshot
 OR (SELECT count(*) FROM transfer_plan WHERE operation_id=op)<>1 OR (SELECT count(*) FROM transfer_step WHERE plan_id=p.plan_id)<>1
 OR plan->>'operation_id' IS DISTINCT FROM op::text OR plan->>'root_id' IS DISTINCT FROM a.root_id::text
 OR plan->>'state' IS DISTINCT FROM 'review_required' OR plan->>'strategy' IS DISTINCT FROM 'hardlink'
 OR plan->'requires_approval' IS DISTINCT FROM 'true'::jsonb OR plan->>'old_file_id' IS DISTINCT FROM a.old_file_id::text
 OR plan->>'execution_token' IS DISTINCT FROM '0' OR plan->>'execution_owner' IS NOT NULL OR plan->>'lease_until' IS NOT NULL
 OR plan->>'root_path' IS DISTINCT FROM physical->>'root_path' OR physical->>'root_path' IS DISTINCT FROM a.snapshot->>'root_path'
 OR plan->>'root_device' IS DISTINCT FROM physical->'root'->>'Device' OR plan->>'root_inode' IS DISTINCT FROM physical->'root'->>'Inode'
 OR step->>'plan_id' IS DISTINCT FROM p.plan_id::text OR step->>'unit_id' IS DISTINCT FROM a.unit_id::text
 OR step->>'state' IS DISTINCT FROM 'pending' OR step->>'action' IS DISTINCT FROM 'hardlink' OR step->>'step_order' IS DISTINCT FROM '1'
 OR step->>'source_root' IS DISTINCT FROM physical->>'source_root' OR step->>'source_rel' IS DISTINCT FROM physical->>'source_rel'
 OR step->>'target_rel' IS DISTINCT FROM physical->>'target_rel'
 OR step->>'source_path' IS DISTINCT FROM (physical->>'source_root')||'/'||(physical->>'source_rel')
 OR step->>'target_path' IS DISTINCT FROM (physical->>'root_path')||'/'||(physical->>'target_rel')
 OR step->>'source_device' IS DISTINCT FROM physical->'source'->>'Device' OR step->>'source_inode' IS DISTINCT FROM physical->'source'->>'Inode'
 OR physical->'source'->>'Device' IS DISTINCT FROM physical->'root'->>'Device'
 OR oldfile.id IS NULL OR physical->>'old_rel' IS DISTINCT FROM oldfile.rel_path
 OR physical->'old'->>'Size' IS DISTINCT FROM oldfile.size_bytes::text OR physical->'old'->>'Mtime' IS DISTINCT FROM oldfile.mtime_ns::text
 OR physical->'old'->>'Device' IS DISTINCT FROM physical->'root'->>'Device'
 OR oldfile.file_fingerprint IS DISTINCT FROM 'stat.v1:'||(physical->'old'->>'Inode')||':'||(physical->'old'->>'Size')||':'||(physical->'old'->>'Mtime')
 OR physical->'old'->>'Inode' IS NOT DISTINCT FROM physical->'source'->>'Inode'
 OR plan->>'root_generation' IS DISTINCT FROM (SELECT generation::text FROM library_root WHERE id=a.root_id)
 OR (physical->'source'->>'Inode')::bigint<=0 OR (physical->'root'->>'Inode')::bigint<=0
 OR step->>'source_size' IS DISTINCT FROM physical->'source'->>'Size' OR step->>'source_mtime' IS DISTINCT FROM physical->'source'->>'Mtime'
 OR (step->>'source_size')::bigint<=0
 OR step->'quality_profile' IS DISTINCT FROM a.snapshot->'candidate_profile'
 OR step->'target_evidence' IS DISTINCT FROM jsonb_build_object('kind','movie_upgrade_organization_prepared_v1','preparation_id',p.id,'authorization_id',a.id,'input_stamp',encode(a.input_sha256,'hex'))
 THEN RAISE EXCEPTION 'upgrade physical plan binding mismatch' USING ERRCODE='23514'; END IF;
 PERFORM pulse_assert_upgrade_preparation(op);
END $$;
CREATE FUNCTION pulse_upgrade_organization_preparation_insert() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE a movie_upgrade_organization_authorization%ROWTYPE; basis JSONB;
BEGIN
 SELECT * INTO a FROM movie_upgrade_organization_authorization WHERE id=NEW.authorization_id;
 basis:=pulse_upgrade_organization_basis(NEW.operation_id,a.observation_id);
 IF basis IS NULL OR basis IS DISTINCT FROM a.snapshot OR NEW.snapshot->'basis' IS DISTINCT FROM basis
 OR NEW.prepared_at>clock_timestamp() OR clock_timestamp()>=NEW.observed_at+interval '30 seconds'
 THEN RAISE EXCEPTION 'upgrade physical preparation evidence changed' USING ERRCODE='23514'; END IF;
 PERFORM pulse_assert_upgrade_organization_preparation(NEW.operation_id);
 RETURN NULL;
END $$;
CREATE FUNCTION pulse_upgrade_organization_transfer_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE op UUID;
BEGIN
 IF TG_TABLE_NAME='transfer_plan' THEN
  IF TG_OP<>'INSERT' THEN PERFORM pulse_assert_upgrade_organization_preparation(OLD.operation_id); END IF;
  IF TG_OP<>'DELETE' THEN PERFORM pulse_assert_upgrade_organization_preparation(NEW.operation_id); END IF;
 ELSE
  IF TG_OP<>'INSERT' THEN SELECT operation_id INTO op FROM transfer_plan WHERE id=OLD.plan_id; IF op IS NOT NULL THEN PERFORM pulse_assert_upgrade_organization_preparation(op); END IF; END IF;
  IF TG_OP<>'DELETE' THEN SELECT operation_id INTO op FROM transfer_plan WHERE id=NEW.plan_id; IF op IS NOT NULL THEN PERFORM pulse_assert_upgrade_organization_preparation(op); END IF; END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER movie_upgrade_organization_preparation_binding AFTER INSERT ON movie_upgrade_organization_preparation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_preparation_insert();
CREATE TRIGGER movie_upgrade_organization_preparation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON movie_upgrade_organization_preparation FOR EACH STATEMENT EXECUTE FUNCTION pulse_upgrade_observation_immutable();
CREATE CONSTRAINT TRIGGER movie_upgrade_organization_plan_binding AFTER INSERT OR UPDATE OR DELETE ON transfer_plan DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_transfer_guard();
CREATE CONSTRAINT TRIGGER movie_upgrade_organization_step_binding AFTER INSERT OR UPDATE OR DELETE ON transfer_step DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_organization_transfer_guard();
REVOKE ALL ON movie_upgrade_organization_preparation FROM PUBLIC;

-- +goose Down
LOCK TABLE movie_upgrade_organization_preparation,movie_upgrade_organization_authorization,transfer_plan,transfer_step,
 movie_upgrade_authority,movie_upgrade_preparation,movie_upgrade_send_job,movie_upgrade_send_job_claim,download_task,
 movie_upgrade_tracking_observation,movie_upgrade_tracking_permit,movie_upgrade_tracking_result,movie_upgrade_tracking_job,movie_upgrade_tracking_job_claim,
 job,app_user,subscription,policy,policy_revision,library_root,library_file,library_file_unit,unit_fact,media_unit,media,media_identity,wanted_item,
 download_client,acquisition_reservation,unit_reservation,download_requirement,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_organization_preparation)
 OR EXISTS(SELECT 1 FROM audit_log WHERE action='upgrade.organization-prepare')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type='upgrade.organization-prepare')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.organization-prepare:%')
 THEN RAISE EXCEPTION 'upgrade physical preparation history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER movie_upgrade_organization_plan_binding ON transfer_plan;
DROP TRIGGER movie_upgrade_organization_step_binding ON transfer_step;
DROP FUNCTION pulse_upgrade_organization_transfer_guard();
DROP TABLE movie_upgrade_organization_preparation;
DROP FUNCTION pulse_upgrade_organization_preparation_insert();
DROP FUNCTION pulse_assert_upgrade_organization_preparation(UUID);
