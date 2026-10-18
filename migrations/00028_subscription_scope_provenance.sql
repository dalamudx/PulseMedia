-- +goose Up
ALTER TABLE subscription ADD COLUMN selected_unit_ids JSONB NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(selected_unit_ids)='array');
ALTER TABLE wanted_item ADD COLUMN user_skipped BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE wanted_item ADD COLUMN scope_excluded BOOLEAN NOT NULL DEFAULT false;
-- Never infer an old skipped record was only a range exclusion; preserve hold.
UPDATE wanted_item SET user_skipped=is_skipped;
UPDATE subscription s SET selected_unit_ids=COALESCE((SELECT jsonb_agg(w.unit_id::text ORDER BY w.unit_id) FROM wanted_item w WHERE w.subscription_id=s.id AND NOT w.is_skipped),'[]'::jsonb) WHERE s.scope_type='selected';
-- +goose StatementBegin
CREATE FUNCTION guard_wanted_skip_provenance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' AND NEW.is_skipped AND NOT NEW.user_skipped AND NOT NEW.scope_excluded THEN NEW.user_skipped:=true; END IF;
 IF TG_OP='UPDATE' AND NEW.is_skipped IS DISTINCT FROM OLD.is_skipped AND NEW.user_skipped=OLD.user_skipped AND NEW.scope_excluded=OLD.scope_excluded THEN NEW.user_skipped:=NEW.is_skipped; END IF;
 NEW.is_skipped:=NEW.user_skipped OR NEW.scope_excluded;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER wanted_skip_provenance BEFORE INSERT OR UPDATE ON wanted_item FOR EACH ROW EXECUTE FUNCTION guard_wanted_skip_provenance();
-- +goose Down
DROP TRIGGER wanted_skip_provenance ON wanted_item;
DROP FUNCTION guard_wanted_skip_provenance();
ALTER TABLE wanted_item DROP COLUMN scope_excluded,DROP COLUMN user_skipped;
ALTER TABLE subscription DROP COLUMN selected_unit_ids;
