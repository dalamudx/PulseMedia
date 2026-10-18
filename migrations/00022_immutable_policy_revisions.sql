-- +goose Up
-- Published revisions are immutable even for accidental direct DML.
-- +goose StatementBegin
CREATE FUNCTION pulse_policy_revision_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'published policy revisions are immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER policy_revision_immutable BEFORE UPDATE OR DELETE ON policy_revision FOR EACH ROW EXECUTE FUNCTION pulse_policy_revision_immutable();
-- +goose Down
DROP TRIGGER policy_revision_immutable ON policy_revision;
DROP FUNCTION pulse_policy_revision_immutable();
