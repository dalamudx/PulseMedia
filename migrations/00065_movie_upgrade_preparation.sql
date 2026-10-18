-- +goose Up
CREATE TABLE movie_upgrade_preparation_lease (
 operation_id UUID PRIMARY KEY
  REFERENCES movie_upgrade_authority(operation_id) ON DELETE RESTRICT,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 owner UUID NOT NULL,
 token UUID NOT NULL UNIQUE,
 state TEXT NOT NULL CHECK(state IN ('leased','prepared')),
 claimed_at TIMESTAMPTZ NOT NULL,
 lease_until TIMESTAMPTZ NOT NULL,
 CHECK(owner <> '00000000-0000-0000-0000-000000000000'::uuid),
 CHECK(token <> '00000000-0000-0000-0000-000000000000'::uuid),
 CHECK(lease_until = claimed_at + interval '60 seconds')
);

CREATE TABLE movie_upgrade_preparation (
 operation_id UUID PRIMARY KEY
  REFERENCES movie_upgrade_authority(operation_id) ON DELETE RESTRICT,
 task_id UUID NOT NULL UNIQUE
  REFERENCES download_task(id) ON DELETE RESTRICT
  DEFERRABLE INITIALLY DEFERRED,
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 160),
 request_hash TEXT NOT NULL,
 owner UUID NOT NULL,
 token UUID NOT NULL,
 status TEXT NOT NULL DEFAULT 'prepared_not_sent'
  CHECK(status='prepared_not_sent'),
 raw_sha256 BYTEA NOT NULL CHECK(octet_length(raw_sha256)=32),
 evidence JSONB NOT NULL CHECK(jsonb_typeof(evidence)='object'),
 task_snapshot JSONB NOT NULL CHECK(jsonb_typeof(task_snapshot)='object'),
 prepared_at TIMESTAMPTZ NOT NULL,
 UNIQUE(actor_id,request_key)
);

CREATE TRIGGER movie_upgrade_preparation_immutable
 BEFORE UPDATE OR DELETE ON movie_upgrade_preparation
 FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();

-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_preparation_lease_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'upgrade preparation lease history is retained';
 END IF;

 IF TG_OP='INSERT' THEN
  IF NEW.state<>'leased' THEN
   RAISE EXCEPTION 'upgrade preparation lease must start leased';
  END IF;
  RETURN NEW;
 END IF;

 IF OLD.state<>'leased'
    OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
    OR NEW.actor_id IS DISTINCT FROM OLD.actor_id THEN
  RAISE EXCEPTION 'upgrade preparation lease is fenced';
 END IF;

 IF NEW.state='prepared' THEN
  IF NEW.owner IS DISTINCT FROM OLD.owner
     OR NEW.token IS DISTINCT FROM OLD.token
     OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at
     OR NEW.lease_until IS DISTINCT FROM OLD.lease_until
     OR OLD.lease_until<=clock_timestamp() THEN
   RAISE EXCEPTION 'upgrade preparation lease expired or changed';
  END IF;
 ELSIF NEW.state='leased' THEN
  IF OLD.lease_until>clock_timestamp()
     OR NEW.token=OLD.token
     OR NEW.claimed_at<OLD.lease_until THEN
   RAISE EXCEPTION 'upgrade preparation lease cannot be renewed';
  END IF;
 ELSE
  RAISE EXCEPTION 'invalid upgrade preparation lease transition';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER movie_upgrade_preparation_lease_guard
 BEFORE INSERT OR UPDATE OR DELETE ON movie_upgrade_preparation_lease
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_preparation_lease_guard();

-- Checks the final graph, rather than depending on INSERT order.
-- +goose StatementBegin
CREATE FUNCTION pulse_assert_upgrade_preparation(op UUID)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
 a movie_upgrade_authority%ROWTYPE;
 l movie_upgrade_preparation_lease%ROWTYPE;
 p movie_upgrade_preparation%ROWTYPE;
 task JSONB;
 n BIGINT;
BEGIN
 SELECT * INTO a FROM movie_upgrade_authority WHERE operation_id=op;
 IF NOT FOUND THEN
  RETURN;
 END IF;

 IF a.goal<>'upgrade' OR a.status<>'reserved_not_executable' THEN
  RAISE EXCEPTION 'upgrade authority is not frozen';
 END IF;

 IF EXISTS(SELECT 1 FROM job WHERE payload->>'operation_id'=op::text) THEN
  RAISE EXCEPTION 'upgrade preparation cannot create jobs';
 END IF;

 SELECT count(*) INTO n FROM download_task WHERE operation_id=op;
 SELECT * INTO p FROM movie_upgrade_preparation WHERE operation_id=op;

 IF NOT FOUND THEN
  IF n<>0 OR EXISTS(
   SELECT 1 FROM movie_upgrade_preparation_lease
   WHERE operation_id=op AND state='prepared'
  ) THEN
   RAISE EXCEPTION 'upgrade physical task has no preparation';
  END IF;
  RETURN;
 END IF;

 SELECT * INTO l FROM movie_upgrade_preparation_lease WHERE operation_id=op;
 IF NOT FOUND OR l.state<>'prepared'
    OR l.actor_id<>a.actor_id OR p.actor_id<>a.actor_id
    OR l.owner<>p.owner OR l.token<>p.token
    OR p.prepared_at<l.claimed_at OR p.prepared_at>=l.lease_until
    OR p.evidence->'authority' IS DISTINCT FROM to_jsonb(a)
    OR p.evidence->>'raw_sha256' IS DISTINCT FROM encode(p.raw_sha256,'hex')
    OR p.evidence->>'protected_digest'
       IS DISTINCT FROM a.snapshot->>'protected_digest'
    OR p.evidence->>'root_generation'
       IS DISTINCT FROM a.root_generation::text THEN
  RAISE EXCEPTION 'upgrade preparation binding mismatch';
 END IF;

 SELECT to_jsonb(t) INTO task FROM download_task t WHERE id=p.task_id;
 IF n<>1 OR task IS NULL OR task IS DISTINCT FROM p.task_snapshot
    OR task->>'operation_id' IS DISTINCT FROM op::text
    OR task->>'state' IS DISTINCT FROM 'submitting'
    OR task->>'save_path' IS DISTINCT FROM p.evidence->>'save_path'
    OR task->>'client_id' IS DISTINCT FROM a.client_id::text
    OR task->>'candidate_id' IS DISTINCT FROM a.candidate_id::text
    OR task->>'info_hash' IS DISTINCT FROM p.evidence->'proof'->>'info_hash'
    OR task->>'total_bytes' IS DISTINCT FROM p.evidence->'proof'->>'total_bytes' THEN
  RAISE EXCEPTION 'upgrade preparation task mismatch';
 END IF;

 IF (SELECT count(*) FROM acquisition_reservation WHERE operation_id=op)<>1
    OR (SELECT count(*) FROM unit_reservation WHERE operation_id=op)<>1
    OR (SELECT count(*) FROM download_requirement WHERE operation_id=op)<>1
    OR NOT EXISTS(
     SELECT 1 FROM acquisition_reservation x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'lane'
      AND x.unit_id=a.unit_id AND x.source_root_id=a.root_id
      AND x.source_generation=a.root_generation
      AND x.candidate_id=a.candidate_id
      AND x.source_stamp=a.snapshot->>'source_stamp'
      AND x.acquisition_lane='default'
      AND x.task_id IS NULL AND NOT x.legacy_conflict
      AND x.released_at IS NULL
    )
    OR NOT EXISTS(
     SELECT 1 FROM unit_reservation x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'reservation'
      AND x.unit_id=a.unit_id AND x.root_id=a.root_id
      AND x.candidate_id=a.candidate_id
      AND x.purpose='upgrade' AND x.state='reserved'
      AND x.expires_at IS NULL
    )
    OR NOT EXISTS(
     SELECT 1 FROM download_requirement x
     WHERE x.operation_id=op
      AND to_jsonb(x)=p.evidence->'requirement'
      AND x.unit_id=a.unit_id AND x.root_id=a.root_id
      AND x.actor_type='user' AND x.actor_id=a.actor_id::text
      AND x.state='pending' AND x.released_at IS NULL
      AND x.submission_fence=jsonb_build_object(
       'goal','upgrade','status','reserved_not_executable',
       'authority',a.operation_id,
       'protected_digest',a.snapshot->>'protected_digest',
       'observation',a.snapshot->'observation')
    ) THEN
  RAISE EXCEPTION 'upgrade preparation protection mismatch';
 END IF;
END $$;
-- +goose StatementEnd

DROP TRIGGER upgrade_no_physical_task ON download_task;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_upgrade_no_physical_task()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  PERFORM pulse_assert_upgrade_preparation(OLD.operation_id);
 END IF;
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER upgrade_no_physical_task
 AFTER INSERT OR UPDATE ON download_task
 DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_no_physical_task();

-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_preparation_graph_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pulse_assert_upgrade_preparation(NEW.operation_id);
 RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER movie_upgrade_preparation_graph
 AFTER INSERT ON movie_upgrade_preparation
 DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_preparation_graph_guard();

CREATE CONSTRAINT TRIGGER movie_upgrade_preparation_lease_graph
 AFTER INSERT OR UPDATE ON movie_upgrade_preparation_lease
 DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_preparation_graph_guard();

-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_preparation_no_job()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(
  SELECT 1 FROM movie_upgrade_authority
  WHERE operation_id::text=NEW.payload->>'operation_id'
 ) THEN
  RAISE EXCEPTION 'upgrade preparation cannot create jobs';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER movie_upgrade_preparation_no_job
 BEFORE INSERT OR UPDATE ON job
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_preparation_no_job();

-- +goose Down
LOCK TABLE subscription, movie_upgrade_authority,
 movie_upgrade_preparation_lease, movie_upgrade_preparation,
 download_task, job, acquisition_reservation, unit_reservation,
 download_requirement, audit_log, outbox_event, idempotency_record
 IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_preparation)
    OR EXISTS(SELECT 1 FROM movie_upgrade_preparation_lease)
    OR EXISTS(
     SELECT 1 FROM audit_log
     WHERE action IN (
      'upgrade.prepare'
     )
    ) THEN
  RAISE EXCEPTION 'upgrade mode, lease and preparation history must be empty';
 END IF;
END $$;
-- +goose StatementEnd

DROP TRIGGER movie_upgrade_preparation_no_job ON job;
DROP FUNCTION pulse_upgrade_preparation_no_job();
DROP TRIGGER upgrade_no_physical_task ON download_task;
DROP TABLE movie_upgrade_preparation;
DROP TABLE movie_upgrade_preparation_lease;
DROP FUNCTION pulse_upgrade_preparation_graph_guard();
DROP FUNCTION pulse_upgrade_preparation_lease_guard();
DROP FUNCTION pulse_assert_upgrade_preparation(UUID);

-- Restore precisely the schema64 prohibition.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION pulse_upgrade_no_physical_task()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(
  SELECT 1 FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id
 ) THEN
  RAISE EXCEPTION 'frozen upgrade is reserved_not_executable';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER upgrade_no_physical_task
 BEFORE INSERT OR UPDATE ON download_task
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_no_physical_task();
