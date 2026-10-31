-- +goose Up
CREATE TABLE subtitle_candidate_query (
 id UUID PRIMARY KEY REFERENCES job(id) ON DELETE RESTRICT,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 file_id UUID NOT NULL REFERENCES library_file(id) ON DELETE RESTRICT,
 mapping_revision BIGINT NOT NULL CHECK(mapping_revision>0),
 command_key TEXT NOT NULL CHECK(command_key ~ '^[A-Za-z0-9:_.-]{1,80}$'),
 outcome TEXT NOT NULL CHECK(outcome IN ('querying','candidates','not_found','already_present','too_small','provider_error','confirming','confirmed','review_required')),
 snapshot JSONB NOT NULL,
 confirm_permit BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(owner_id,command_key)
);
CREATE INDEX subtitle_candidate_query_file ON subtitle_candidate_query(owner_id,file_id,created_at DESC,id DESC);
CREATE TABLE subtitle_candidate (
 id UUID PRIMARY KEY,
 query_id UUID NOT NULL REFERENCES subtitle_candidate_query(id) ON DELETE RESTRICT,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0 AND ordinal<8),
 content_encrypted BYTEA NOT NULL CHECK(octet_length(content_encrypted)>28 AND octet_length(content_encrypted)<=2097248),
 UNIQUE(query_id,ordinal)
);
ALTER TABLE subtitle_candidate_query ADD COLUMN selected_candidate_id UUID REFERENCES subtitle_candidate(id) ON DELETE RESTRICT;
ALTER TABLE subtitle_candidate_query ADD CONSTRAINT subtitle_confirmation_selection CHECK(confirm_permit=(selected_candidate_id IS NOT NULL));
-- +goose StatementBegin
CREATE FUNCTION pulse_subtitle_candidate_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' OR ROW(OLD.id,OLD.owner_id,OLD.file_id,OLD.mapping_revision,OLD.command_key,OLD.snapshot,OLD.created_at) IS DISTINCT FROM ROW(NEW.id,NEW.owner_id,NEW.file_id,NEW.mapping_revision,NEW.command_key,NEW.snapshot,NEW.created_at) OR OLD.confirm_permit AND (NOT NEW.confirm_permit OR OLD.selected_candidate_id IS DISTINCT FROM NEW.selected_candidate_id) THEN
  RAISE EXCEPTION 'subtitle candidate authority is immutable' USING ERRCODE='23514';
 END IF;
 IF NOT ((OLD.outcome='querying' AND NEW.outcome IN ('candidates','not_found','already_present','too_small','provider_error','review_required') AND NOT NEW.confirm_permit)
 OR (OLD.outcome='candidates' AND NEW.outcome='confirming' AND NOT OLD.confirm_permit AND NEW.confirm_permit AND EXISTS(SELECT 1 FROM subtitle_candidate WHERE id=NEW.selected_candidate_id AND query_id=NEW.id))
 OR (OLD.outcome='confirming' AND NEW.outcome IN ('confirmed','already_present','review_required') AND NEW.confirm_permit)) THEN
  RAISE EXCEPTION 'invalid subtitle candidate transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER subtitle_candidate_guard BEFORE UPDATE OR DELETE ON subtitle_candidate_query FOR EACH ROW EXECUTE FUNCTION pulse_subtitle_candidate_guard();
CREATE TRIGGER subtitle_candidate_query_no_truncate BEFORE TRUNCATE ON subtitle_candidate_query FOR EACH STATEMENT EXECUTE FUNCTION pulse_subtitle_candidate_guard();
CREATE FUNCTION pulse_subtitle_candidate_content_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'subtitle candidate content is immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER subtitle_candidate_content_guard BEFORE UPDATE OR DELETE ON subtitle_candidate FOR EACH ROW EXECUTE FUNCTION pulse_subtitle_candidate_content_guard();
CREATE TRIGGER subtitle_candidate_content_no_truncate BEFORE TRUNCATE ON subtitle_candidate FOR EACH STATEMENT EXECUTE FUNCTION pulse_subtitle_candidate_content_guard();
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM subtitle_candidate_query) THEN RAISE EXCEPTION 'subtitle candidate history prevents downgrade'; END IF; END $$;
-- +goose StatementEnd
ALTER TABLE subtitle_candidate_query DROP COLUMN selected_candidate_id;
DROP TABLE subtitle_candidate;
DROP TABLE subtitle_candidate_query;
DROP FUNCTION pulse_subtitle_candidate_guard();
DROP FUNCTION pulse_subtitle_candidate_content_guard();
