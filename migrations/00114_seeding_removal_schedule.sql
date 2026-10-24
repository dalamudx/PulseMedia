-- +goose Up
-- Queue intent only. A dispatch cannot grant physical removal authority.
CREATE TABLE seeding_removal_schedule (
 task_id UUID PRIMARY KEY REFERENCES download_task(id),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 next_due_at TIMESTAMPTZ NOT NULL,
 active_job_id UUID UNIQUE REFERENCES job(id),
 state TEXT NOT NULL CHECK(state IN ('idle','queued','running','review_required','retired')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK((state IN ('queued','running'))=(active_job_id IS NOT NULL))
);
CREATE TABLE seeding_removal_dispatch (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 task_id UUID NOT NULL REFERENCES download_task(id),
 client_id UUID NOT NULL REFERENCES download_client(id),
 client_stamp TEXT NOT NULL CHECK(client_stamp COLLATE "C" ~ '^[0-9a-f]{64}$'),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 owner_id UUID NOT NULL REFERENCES app_user(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX seeding_dispatch_task_history ON seeding_removal_dispatch(task_id,created_at,job_id);
-- +goose StatementBegin
CREATE FUNCTION pulse_seeding_dispatch_history() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'seeding dispatch history protected' USING ERRCODE='23514';
END;
$$;
CREATE FUNCTION pulse_seeding_schedule_history() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'seeding schedule history protected' USING ERRCODE='23514'; END IF;
 IF NEW.task_id<>OLD.task_id OR (OLD.state IN ('review_required','retired') AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'seeding schedule identity or isolation protected' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE FUNCTION pulse_seeding_dispatch_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE t download_task%ROWTYPE; c seeding_configuration%ROWTYPE; j job%ROWTYPE; s seeding_removal_schedule%ROWTYPE; proof RECORD;
BEGIN
 IF EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated) THEN RAISE EXCEPTION 'restore isolation active' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM app_user WHERE id=NEW.owner_id FOR SHARE;
 PERFORM 1 FROM download_client WHERE id=NEW.client_id FOR SHARE;
 SELECT * INTO STRICT c FROM seeding_configuration WHERE client_id=NEW.client_id FOR SHARE;
 SELECT * INTO STRICT t FROM download_task WHERE id=NEW.task_id;
 PERFORM pg_advisory_xact_lock(hashtextextended('acquisition:'||t.operation_id::text,0));
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id;
 SELECT * INTO STRICT s FROM seeding_removal_schedule WHERE task_id=NEW.task_id;
 IF c.owner_id<>NEW.owner_id OR NOT c.enabled OR c.version<>NEW.config_version OR c.client_stamp<>NEW.client_stamp
 OR t.client_id<>NEW.client_id OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=NEW.owner_id AND is_active AND role='admin')
 OR EXISTS(SELECT 1 FROM seeding_removal_permit WHERE task_id=t.id)
 OR j.kind<>'seeding_removal' OR j.state<>'queued' OR j.max_attempts<>3 OR j.attempts<>0 OR j.lease_token<>0 OR j.lease_owner IS NOT NULL
 OR j.payload IS DISTINCT FROM jsonb_build_object('task_id',t.id,'client_id',t.client_id,'config_version',c.version)
 OR s.active_job_id IS DISTINCT FROM j.id OR s.state<>'queued' OR s.config_version<>c.version THEN
  RAISE EXCEPTION 'seeding dispatch authority or job graph changed' USING ERRCODE='23514';
 END IF;
 SELECT * INTO STRICT proof FROM pulse_ordinary_seeding_protection(t.id,NEW.client_stamp);
 IF NOT proof.owned OR NOT proof.imported OR proof.pending_work THEN RAISE EXCEPTION 'seeding dispatch ingestion proof unavailable' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_log WHERE action='seeding.removal.queued' AND target_type='download_task' AND target_id=t.id::text AND payload->>'job_id'=j.id::text)
 OR NOT EXISTS(SELECT 1 FROM outbox_event WHERE aggregate_type='seeding.removal' AND aggregate_id=t.id::text AND event_type='seeding.removal.queued' AND payload->>'job_id'=j.id::text) THEN
  RAISE EXCEPTION 'seeding dispatch success ledger missing' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER seeding_dispatch_history BEFORE UPDATE OR DELETE ON seeding_removal_dispatch FOR EACH ROW EXECUTE FUNCTION pulse_seeding_dispatch_history();
CREATE TRIGGER seeding_dispatch_truncate BEFORE TRUNCATE ON seeding_removal_dispatch FOR EACH STATEMENT EXECUTE FUNCTION pulse_seeding_dispatch_history();
CREATE CONSTRAINT TRIGGER seeding_dispatch_commit AFTER INSERT ON seeding_removal_dispatch DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_seeding_dispatch_commit();
CREATE TRIGGER seeding_schedule_update BEFORE UPDATE ON seeding_removal_schedule FOR EACH ROW EXECUTE FUNCTION pulse_seeding_schedule_history();
CREATE TRIGGER seeding_schedule_history BEFORE DELETE OR TRUNCATE ON seeding_removal_schedule FOR EACH STATEMENT EXECUTE FUNCTION pulse_seeding_schedule_history();
REVOKE ALL ON seeding_removal_schedule,seeding_removal_dispatch FROM PUBLIC;
-- +goose Down
LOCK TABLE seeding_removal_schedule,seeding_removal_dispatch IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM seeding_removal_schedule) OR EXISTS(SELECT 1 FROM seeding_removal_dispatch) THEN RAISE EXCEPTION 'seeding scheduling history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE seeding_removal_dispatch,seeding_removal_schedule;
DROP FUNCTION pulse_seeding_dispatch_commit();
DROP FUNCTION pulse_seeding_schedule_history();
DROP FUNCTION pulse_seeding_dispatch_history();
