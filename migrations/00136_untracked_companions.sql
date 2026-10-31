-- +goose Up
CREATE TABLE untracked_companion (
 id UUID PRIMARY KEY,
 operation_id UUID NOT NULL REFERENCES untracked_organization_operation(id),
 command_key TEXT NOT NULL CHECK(length(command_key) BETWEEN 1 AND 160),
 selection_hash TEXT NOT NULL CHECK(selection_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 candidate JSONB NOT NULL CHECK(jsonb_typeof(candidate)='object' AND candidate->>'key' ~ '^[0-9a-f]{64}$'),
 action TEXT NOT NULL CHECK(action IN ('copy','hardlink')),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','unknown','completed','review_required')),
 execution_owner UUID,
 execution_token BIGINT NOT NULL DEFAULT 0 CHECK(execution_token>=0),
 lease_until TIMESTAMPTZ,
 content JSONB,
 permit_owner UUID,
 permit_token BIGINT,
 permit_issued_at TIMESTAMPTZ,
 result JSONB,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK((content IS NULL AND permit_owner IS NULL AND permit_token IS NULL AND permit_issued_at IS NULL) OR (content IS NOT NULL AND permit_owner IS NOT NULL AND permit_token>0 AND permit_issued_at IS NOT NULL)),
 CHECK(state<>'completed' OR (content IS NOT NULL AND result IS NOT NULL)),
 CHECK(state<>'running' OR (execution_owner IS NOT NULL AND execution_token>0 AND lease_until IS NOT NULL))
);
CREATE UNIQUE INDEX untracked_companion_candidate ON untracked_companion(operation_id, (candidate->>'key'));
CREATE INDEX untracked_companion_operation ON untracked_companion(operation_id,created_at,id);
-- +goose StatementBegin
CREATE FUNCTION protect_untracked_companion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'companion history is permanent' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.operation_id,NEW.command_key,NEW.selection_hash,NEW.candidate,NEW.action,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.operation_id,OLD.command_key,OLD.selection_hash,OLD.candidate,OLD.action,OLD.created_at)
 OR (OLD.content IS NOT NULL AND (NEW.content,NEW.permit_owner,NEW.permit_token,NEW.permit_issued_at) IS DISTINCT FROM (OLD.content,OLD.permit_owner,OLD.permit_token,OLD.permit_issued_at))
 OR (OLD.result IS NOT NULL AND NEW.result IS DISTINCT FROM OLD.result)
 OR (OLD.state='completed' AND NEW IS DISTINCT FROM OLD)
 OR NEW.execution_token<OLD.execution_token THEN RAISE EXCEPTION 'companion frozen evidence is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER untracked_companion_immutable BEFORE UPDATE OR DELETE ON untracked_companion FOR EACH ROW EXECUTE FUNCTION protect_untracked_companion();
CREATE TRIGGER untracked_companion_no_truncate BEFORE TRUNCATE ON untracked_companion FOR EACH STATEMENT EXECUTE FUNCTION protect_untracked_companion();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM untracked_companion) THEN RAISE EXCEPTION 'cannot remove permanent companion history' USING ERRCODE='23514'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE untracked_companion;
DROP FUNCTION protect_untracked_companion();
