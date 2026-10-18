-- +goose Up
CREATE TABLE restore_isolation (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton),
 restore_id UUID,
 isolated BOOLEAN NOT NULL DEFAULT false,
 CHECK(NOT isolated OR restore_id IS NOT NULL)
);
INSERT INTO restore_isolation(singleton) VALUES(true);
CREATE TABLE restore_isolation_audit (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 restore_id UUID NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('activate','reconcile')),
 operator_name TEXT NOT NULL CHECK(length(btrim(operator_name)) > 0),
 evidence TEXT NOT NULL CHECK(length(btrim(evidence)) > 0),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION protect_restore_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'restore isolation evidence is immutable'; END;
$$;
-- +goose StatementEnd
CREATE TRIGGER restore_audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON restore_isolation_audit FOR EACH STATEMENT EXECUTE FUNCTION protect_restore_audit();
CREATE TRIGGER restore_state_persistent BEFORE DELETE OR TRUNCATE ON restore_isolation FOR EACH STATEMENT EXECUTE FUNCTION protect_restore_audit();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE restore_isolation, restore_isolation_audit IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM restore_isolation WHERE isolated OR restore_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM restore_isolation_audit) THEN
 RAISE EXCEPTION 'cannot remove restore isolation history'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE restore_isolation_audit;
DROP TABLE restore_isolation;
DROP FUNCTION protect_restore_audit();
