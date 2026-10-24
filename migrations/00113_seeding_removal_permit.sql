-- +goose Up
-- Permanent, task-only retirement evidence. No scheduler or network side effects.
-- +goose StatementBegin
CREATE FUNCTION pulse_ordinary_seeding_protection(UUID,TEXT)
RETURNS TABLE(task_id UUID,owned BOOLEAN,imported BOOLEAN,pending_work BOOLEAN)
LANGUAGE SQL AS $$
SELECT t.id AS task_id,
 EXISTS (
  SELECT 1 FROM download_task_client_binding b JOIN job j ON j.id=b.job_id
  WHERE b.task_id=t.id AND b.client_id=t.client_id
   AND b.client_stamp=$2::text
   AND b.client_stamp=pulse_global_rates_binding(t.client_id)
   AND j.state='succeeded' AND j.kind IN ('download_submission','episode_download_submission')
   AND j.payload->>'operation_id'=t.operation_id::text
   AND j.payload->>'candidate_id'=t.candidate_id::text
 )::boolean AS owned,
 (
  t.state='completed' AND t.organization_state='completed' AND t.total_bytes>0
  AND t.info_hash COLLATE "C" ~ '^[0-9a-f]{40}$'
  AND EXISTS (SELECT 1 FROM transfer_plan p WHERE p.id=t.transfer_plan_id
   AND p.operation_id=t.operation_id AND p.state='completed')
  AND EXISTS (SELECT 1 FROM job j WHERE j.kind='download_organization' AND j.state='succeeded'
   AND j.result->>'task_id'=t.id::text AND j.result->>'plan_id'=t.transfer_plan_id::text)
  AND EXISTS (SELECT 1 FROM download_requirement d WHERE d.operation_id=t.operation_id AND d.state='imported')
  AND NOT EXISTS (
   SELECT 1 FROM download_requirement d WHERE d.operation_id=t.operation_id AND d.state='imported'
   AND NOT EXISTS (
    SELECT 1 FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id
    JOIN library_file f ON f.id=s.target_file_id AND f.root_id=p.root_id AND f.retired_at IS NULL
    JOIN library_file_unit m ON m.file_id=f.id AND m.unit_id=s.unit_id AND m.retired_at IS NULL
    JOIN library_root r ON r.id=p.root_id AND r.health='online' AND r.path=p.root_path AND r.generation=p.root_generation
    JOIN unit_fact fact ON fact.unit_id=s.unit_id AND fact.root_id=p.root_id
     AND fact.source_file_id=f.id AND fact.stage='imported' AND fact.revoked_at IS NULL
     AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp())
    WHERE p.operation_id=t.operation_id AND p.root_id=d.root_id AND p.state='completed'
     AND s.unit_id=d.unit_id AND s.state='completed' AND s.source_size>0 AND f.size_bytes=s.source_size
   )
  )
  AND NOT EXISTS (SELECT 1 FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id
   WHERE p.operation_id=t.operation_id AND (p.state<>'completed' OR s.state<>'completed'))
 )::boolean AS imported,
 (
  EXISTS (SELECT 1 FROM download_requirement d WHERE d.operation_id=t.operation_id AND d.state='pending')
  OR EXISTS (SELECT 1 FROM unit_reservation r WHERE r.operation_id=t.operation_id AND r.state IN ('reserved','dispatched'))
  OR EXISTS (SELECT 1 FROM acquisition_reservation a WHERE a.operation_id=t.operation_id AND a.released_at IS NULL)
  OR EXISTS (SELECT 1 FROM transfer_plan p WHERE p.operation_id=t.operation_id AND p.state<>'completed')
  OR EXISTS (SELECT 1 FROM job j WHERE j.kind IN ('download_submission','episode_download_submission','download_organization')
    AND j.state IN ('queued','running') AND (j.payload->>'operation_id'=t.operation_id::text OR j.payload->>'task_id'=t.id::text))
 )::boolean AS pending_work
FROM download_task t WHERE t.id=$1::uuid;
$$;
-- +goose StatementEnd
CREATE TABLE seeding_removal_permit (
 task_id UUID PRIMARY KEY REFERENCES download_task(id),
 operation_id UUID NOT NULL UNIQUE,
 client_id UUID NOT NULL REFERENCES download_client(id),
 client_stamp TEXT NOT NULL CHECK(client_stamp COLLATE "C" ~ '^[0-9a-f]{64}$'),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 owner_id UUID NOT NULL REFERENCES app_user(id),
 job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 lease_owner TEXT NOT NULL CHECK(length(lease_owner) BETWEEN 1 AND 160),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 info_hash TEXT NOT NULL CHECK(info_hash COLLATE "C" ~ '^[0-9a-f]{40}$'),
 operation_tag TEXT NOT NULL,
 save_path TEXT NOT NULL CHECK(length(save_path)>0),
 total_bytes BIGINT NOT NULL CHECK(total_bytes>0),
 complete BOOLEAN NOT NULL CHECK(complete),
 seeding_seconds BIGINT CHECK(seeding_seconds>=0),
 share_ratio DOUBLE PRECISION CHECK(share_ratio>=0 AND share_ratio<'Infinity'::double precision),
 infinite_share_ratio BOOLEAN NOT NULL,
 free_bytes BIGINT CHECK(free_bytes>=0),
 free_space_path TEXT,
 observed_at TIMESTAMPTZ NOT NULL,
 prepared_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(NOT infinite_share_ratio OR share_ratio IS NULL),
 CHECK((free_bytes IS NULL)=(free_space_path IS NULL)),
 CHECK(operation_tag='pulsemedia:op:'||operation_id::text),
 CHECK(observed_at<=prepared_at)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_seeding_removal_history() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'permanent seeding permit history protected' USING ERRCODE='23514';
END;
$$;
CREATE FUNCTION pulse_seeding_operation_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE op UUID;
BEGIN
 IF TG_OP='DELETE' THEN op:=OLD.operation_id; ELSE op:=NEW.operation_id; END IF;
 -- Use the existing acquisition lock, also taken by retirement admission.
 PERFORM pg_advisory_xact_lock(hashtextextended('acquisition:'||op::text,0));
 IF EXISTS(SELECT 1 FROM seeding_removal_permit WHERE operation_id=op)
 OR (TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM seeding_removal_permit WHERE operation_id=OLD.operation_id)) THEN
  RAISE EXCEPTION 'retired download operation cannot acquire new work' USING ERRCODE='23514';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_seeding_job_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE t RECORD;
BEGIN
 IF TG_OP='UPDATE' AND OLD.kind=NEW.kind AND OLD.payload IS NOT DISTINCT FROM NEW.payload AND OLD.state IN ('queued','running') THEN RETURN NEW; END IF;
 IF NEW.kind NOT IN ('download_submission','episode_download_submission','download_organization') OR NEW.state NOT IN ('queued','running') THEN RETURN NEW; END IF;
 FOR t IN SELECT id,operation_id FROM download_task
  WHERE operation_id::text=NEW.payload->>'operation_id' OR id::text=NEW.payload->>'task_id' ORDER BY id LOOP
  PERFORM pg_advisory_xact_lock(hashtextextended('acquisition:'||t.operation_id::text,0));
  IF EXISTS(SELECT 1 FROM seeding_removal_permit WHERE task_id=t.id) THEN
   RAISE EXCEPTION 'retired download task cannot restart work' USING ERRCODE='23514';
  END IF;
 END LOOP;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_assert_seeding_removal_permit() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE t download_task%ROWTYPE; c seeding_configuration%ROWTYPE; j job%ROWTYPE;
 u app_user%ROWTYPE; proof RECORD; criteria BOOLEAN[]:=ARRAY[]::BOOLEAN[]; eligible BOOLEAN;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('acquisition:'||NEW.operation_id::text,0));
 SELECT * INTO STRICT u FROM app_user WHERE id=NEW.owner_id FOR SHARE;
 SELECT * INTO STRICT c FROM seeding_configuration WHERE client_id=NEW.client_id FOR SHARE;
 PERFORM 1 FROM download_client WHERE id=NEW.client_id FOR SHARE;
 SELECT * INTO STRICT t FROM download_task WHERE id=NEW.task_id FOR SHARE;
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id FOR SHARE;
 IF NOT u.is_active OR u.role<>'admin' OR NOT c.enabled OR c.owner_id<>u.id
 OR c.version<>NEW.config_version OR c.client_stamp<>NEW.client_stamp
 OR t.operation_id<>NEW.operation_id OR t.client_id<>NEW.client_id
 OR t.info_hash<>NEW.info_hash OR t.save_path<>NEW.save_path OR t.total_bytes<>NEW.total_bytes
 OR j.kind<>'seeding_removal' OR j.state<>'running' OR j.max_attempts<>3
 OR j.lease_owner IS DISTINCT FROM NEW.lease_owner OR j.lease_token<>NEW.lease_token
 OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
 OR j.payload IS DISTINCT FROM jsonb_build_object('task_id',t.id,'client_id',t.client_id,'config_version',c.version)
 OR NEW.observed_at>clock_timestamp() OR NEW.observed_at<clock_timestamp()-interval '30 seconds'
 OR NEW.prepared_at>clock_timestamp() THEN
  RAISE EXCEPTION 'seeding permit authority expired or changed' USING ERRCODE='23514';
 END IF;
 -- Protect existing positive proof rows through commit. Operation guards above
 -- serialize newly inserted demands/plans/reservations against this admission.
 PERFORM p.id FROM transfer_plan p WHERE p.operation_id=t.operation_id ORDER BY p.id FOR SHARE;
 PERFORM r.id FROM library_root r WHERE EXISTS(SELECT 1 FROM transfer_plan p WHERE p.operation_id=t.operation_id AND p.root_id=r.id) ORDER BY r.id FOR SHARE;
 PERFORM s.id FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id WHERE p.operation_id=t.operation_id ORDER BY s.id FOR SHARE OF s;
 PERFORM f.id FROM library_file f WHERE EXISTS(SELECT 1 FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id WHERE p.operation_id=t.operation_id AND s.target_file_id=f.id) ORDER BY f.id FOR SHARE;
 PERFORM m.file_id FROM library_file_unit m WHERE EXISTS(SELECT 1 FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id WHERE p.operation_id=t.operation_id AND s.target_file_id=m.file_id) ORDER BY m.file_id,m.unit_id FOR SHARE;
 PERFORM f.id FROM unit_fact f WHERE EXISTS(SELECT 1 FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id WHERE p.operation_id=t.operation_id AND s.target_file_id=f.source_file_id) ORDER BY f.id FOR SHARE;
 SELECT * INTO STRICT proof FROM pulse_ordinary_seeding_protection(t.id,NEW.client_stamp);
 IF NOT proof.owned OR NOT proof.imported OR proof.pending_work THEN
  RAISE EXCEPTION 'seeding permit lacks completed original ingestion' USING ERRCODE='23514';
 END IF;
 IF c.min_seeding_seconds IS NOT NULL THEN criteria:=array_append(criteria,NEW.seeding_seconds>=c.min_seeding_seconds); END IF;
 IF c.min_share_ratio IS NOT NULL THEN criteria:=array_append(criteria,NEW.infinite_share_ratio OR NEW.share_ratio>=c.min_share_ratio); END IF;
 IF c.low_free_bytes IS NOT NULL THEN criteria:=array_append(criteria,NEW.free_space_path=NEW.save_path AND NEW.free_bytes<c.low_free_bytes); END IF;
 SELECT CASE WHEN c.match_mode='all' THEN bool_and(COALESCE(x,FALSE)) ELSE bool_or(COALESCE(x,FALSE)) END INTO eligible FROM unnest(criteria) x;
 IF eligible IS DISTINCT FROM TRUE THEN
  RAISE EXCEPTION 'seeding thresholds not established' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_log a WHERE a.action='seeding.removal.prepared' AND a.target_type='download_task' AND a.target_id=t.id::text AND a.payload->>'job_id'=j.id::text)
 OR NOT EXISTS(SELECT 1 FROM outbox_event e WHERE e.aggregate_type='seeding.removal' AND e.aggregate_id=t.id::text AND e.event_type='seeding.removal.prepared' AND e.payload->>'job_id'=j.id::text) THEN
  RAISE EXCEPTION 'seeding permit success ledger missing' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER seeding_removal_history BEFORE UPDATE OR DELETE ON seeding_removal_permit FOR EACH ROW EXECUTE FUNCTION pulse_seeding_removal_history();
CREATE TRIGGER seeding_removal_truncate BEFORE TRUNCATE ON seeding_removal_permit FOR EACH STATEMENT EXECUTE FUNCTION pulse_seeding_removal_history();
CREATE CONSTRAINT TRIGGER seeding_removal_assert AFTER INSERT ON seeding_removal_permit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_assert_seeding_removal_permit();
CREATE TRIGGER seeding_requirement_guard BEFORE INSERT OR UPDATE OR DELETE ON download_requirement FOR EACH ROW EXECUTE FUNCTION pulse_seeding_operation_guard();
CREATE TRIGGER seeding_reservation_guard BEFORE INSERT OR UPDATE OR DELETE ON unit_reservation FOR EACH ROW EXECUTE FUNCTION pulse_seeding_operation_guard();
CREATE TRIGGER seeding_acquisition_guard BEFORE INSERT OR UPDATE OR DELETE ON acquisition_reservation FOR EACH ROW EXECUTE FUNCTION pulse_seeding_operation_guard();
CREATE TRIGGER seeding_plan_guard BEFORE INSERT OR UPDATE OR DELETE ON transfer_plan FOR EACH ROW EXECUTE FUNCTION pulse_seeding_operation_guard();
CREATE TRIGGER seeding_job_guard BEFORE INSERT OR UPDATE ON job FOR EACH ROW EXECUTE FUNCTION pulse_seeding_job_guard();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM seeding_removal_permit) THEN
  RAISE EXCEPTION 'permanent seeding permit history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER seeding_job_guard ON job;
DROP TRIGGER seeding_plan_guard ON transfer_plan;
DROP TRIGGER seeding_acquisition_guard ON acquisition_reservation;
DROP TRIGGER seeding_reservation_guard ON unit_reservation;
DROP TRIGGER seeding_requirement_guard ON download_requirement;
DROP TABLE seeding_removal_permit;
DROP FUNCTION pulse_assert_seeding_removal_permit();
DROP FUNCTION pulse_seeding_job_guard();
DROP FUNCTION pulse_seeding_operation_guard();
DROP FUNCTION pulse_seeding_removal_history();
DROP FUNCTION pulse_ordinary_seeding_protection(UUID,TEXT);
