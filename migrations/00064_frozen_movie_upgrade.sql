-- +goose Up
-- Existing subscriptions remain disabled. replace_if_better is deliberately not
-- admitted until its separate evaluation/execution contract exists.
ALTER TABLE subscription ADD COLUMN upgrade_mode TEXT NOT NULL DEFAULT 'disabled'
 CHECK (upgrade_mode IN ('disabled','stop_at_cutoff'));
-- Every change invalidates observations, Wanted stamps and prior authorization.
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_mode_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.upgrade_mode IS DISTINCT FROM OLD.upgrade_mode THEN
  NEW.intent_version := GREATEST(NEW.intent_version,OLD.intent_version+1);
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER subscription_upgrade_mode_revision BEFORE UPDATE ON subscription
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_mode_revision();

CREATE TABLE movie_upgrade_authority (
 operation_id UUID PRIMARY KEY,
 goal TEXT NOT NULL DEFAULT 'upgrade' CHECK(goal='upgrade'),
 status TEXT NOT NULL DEFAULT 'reserved_not_executable' CHECK(status='reserved_not_executable'),
 actor_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 subscription_id UUID NOT NULL REFERENCES subscription(id) ON DELETE RESTRICT,
 intent_version BIGINT NOT NULL CHECK(intent_version>0),
 unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
 root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
 root_generation BIGINT NOT NULL,
 old_file_id UUID NOT NULL REFERENCES library_file(id) ON DELETE RESTRICT,
 old_fact_id UUID NOT NULL REFERENCES unit_fact(id) ON DELETE RESTRICT,
 candidate_id UUID NOT NULL REFERENCES release_candidate(id) ON DELETE RESTRICT,
 client_id UUID NOT NULL REFERENCES download_client(id) ON DELETE RESTRICT,
 policy_id TEXT NOT NULL,
 policy_revision BIGINT NOT NULL,
 FOREIGN KEY(policy_id,policy_revision) REFERENCES policy_revision(policy_id,revision) ON DELETE RESTRICT,
 snapshot JSONB NOT NULL CHECK(COALESCE(jsonb_typeof(snapshot)='object' AND snapshot @> '{"kind":"frozen_movie_upgrade_v1","goal":"upgrade","confirmed":true}'::jsonb,false)),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER movie_upgrade_authority_immutable BEFORE UPDATE OR DELETE ON movie_upgrade_authority
 FOR EACH ROW EXECUTE FUNCTION pulse_episode_candidate_review_guard();
-- Defence in depth: even an accidental generic caller cannot create physical
-- work for this non-executable slice. This does not replace the entry guards.
-- +goose StatementBegin
CREATE FUNCTION pulse_upgrade_no_physical_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id) THEN
  RAISE EXCEPTION 'frozen upgrade is reserved_not_executable';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER upgrade_no_physical_task BEFORE INSERT OR UPDATE ON download_task
 FOR EACH ROW EXECUTE FUNCTION pulse_upgrade_no_physical_task();

-- +goose Down
-- Lock BEFORE testing history: concurrent writers cannot enter the check/drop gap.
LOCK TABLE subscription, movie_upgrade_authority, audit_log IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM movie_upgrade_authority) OR EXISTS(
  SELECT 1 FROM audit_log WHERE action IN ('subscription.upgrade_mode','upgrade.reserve')) THEN
  RAISE EXCEPTION 'frozen upgrade and mode history cannot be discarded';
 END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER upgrade_no_physical_task ON download_task;
DROP FUNCTION pulse_upgrade_no_physical_task();
DROP TABLE movie_upgrade_authority;
DROP TRIGGER subscription_upgrade_mode_revision ON subscription;
DROP FUNCTION pulse_upgrade_mode_revision();
ALTER TABLE subscription DROP COLUMN upgrade_mode;
