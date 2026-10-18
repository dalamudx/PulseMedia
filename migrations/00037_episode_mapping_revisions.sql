-- +goose Up
CREATE TABLE episode_mapping_revision(
 subscription_id UUID NOT NULL REFERENCES subscription(id), revision BIGINT NOT NULL CHECK(revision>0), media_id UUID NOT NULL REFERENCES media(id), media_version BIGINT NOT NULL CHECK(media_version>0), intent_version BIGINT NOT NULL CHECK(intent_version>0), mode TEXT NOT NULL CHECK(mode IN ('standard','group','absolute','manual')), group_id TEXT NOT NULL DEFAULT '', part_id TEXT NOT NULL DEFAULT '', input_stamp TEXT NOT NULL CHECK(length(input_stamp)=64), source_hash TEXT NOT NULL CHECK(length(source_hash)=64), source_snapshot JSONB NOT NULL, mappings JSONB NOT NULL CHECK(jsonb_typeof(mappings)='array' AND jsonb_array_length(mappings)<=5000), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), valid_until TIMESTAMPTZ, PRIMARY KEY(subscription_id,revision), CHECK((mode IN ('group','absolute') AND group_id<>'' AND part_id<>'' AND valid_until IS NOT NULL) OR (mode IN ('manual','standard') AND group_id='' AND part_id='' AND valid_until IS NULL)), CHECK((mode='standard' AND jsonb_array_length(mappings)=0) OR (mode<>'standard' AND jsonb_array_length(mappings)>0)));
ALTER TABLE subscription ADD COLUMN episode_mapping_revision BIGINT;
ALTER TABLE subscription ADD CONSTRAINT episode_mapping_pointer FOREIGN KEY(id,episode_mapping_revision) REFERENCES episode_mapping_revision(subscription_id,revision);
-- +goose StatementBegin
CREATE FUNCTION pulse_episode_mapping_history() RETURNS trigger LANGUAGE plpgsql AS $$DECLARE item JSONB;BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'episode mapping revisions are immutable';END IF;
 IF NOT EXISTS(SELECT 1 FROM subscription s JOIN media m ON m.id=s.media_id WHERE s.id=NEW.subscription_id AND s.media_id=NEW.media_id AND m.media_type='tv' AND m.metadata_version=NEW.media_version AND s.intent_version+1=NEW.intent_version) THEN RAISE EXCEPTION 'episode mapping parent version mismatch';END IF;
 IF (SELECT count(DISTINCT (v->>'source_number')::integer) FROM jsonb_array_elements(NEW.mappings) v)<>jsonb_array_length(NEW.mappings) OR (SELECT count(DISTINCT v->>'unit_id') FROM jsonb_array_elements(NEW.mappings) v)<>jsonb_array_length(NEW.mappings) THEN RAISE EXCEPTION 'episode mapping is ambiguous';END IF;
 FOR item IN SELECT * FROM jsonb_array_elements(NEW.mappings) LOOP
 IF NOT ((item->>'source_number')::integer BETWEEN 1 AND 9999) OR NOT EXISTS(SELECT 1 FROM media_unit u WHERE u.id=(item->>'unit_id')::uuid AND u.media_id=NEW.media_id AND NOT u.retired AND u.unit_key=item->>'unit_key' AND u.metadata_version=(item->>'unit_version')::bigint) THEN RAISE EXCEPTION 'episode mapping target mismatch';END IF;
 IF NEW.mode IN ('group','absolute') AND NOT EXISTS(SELECT 1 FROM media_unit_identity i JOIN media_identity p ON p.media_id=NEW.media_id AND p.namespace='tmdb.tv' AND p.external_id=i.tmdb_series_id::text WHERE i.unit_id=(item->>'unit_id')::uuid AND i.namespace='tmdb.episode' AND i.external_id=(item->>'episode_id')::bigint) THEN RAISE EXCEPTION 'episode mapping source identity mismatch';END IF;
 END LOOP;RETURN NEW;END$$;
-- +goose StatementEnd
CREATE TRIGGER episode_mapping_history BEFORE INSERT OR UPDATE OR DELETE ON episode_mapping_revision FOR EACH ROW EXECUTE FUNCTION pulse_episode_mapping_history();
-- +goose StatementBegin
CREATE FUNCTION pulse_episode_mapping_binding() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF NEW.episode_mapping_revision IS DISTINCT FROM OLD.episode_mapping_revision THEN
 IF NEW.episode_mapping_revision IS NULL OR NEW.episode_mapping_revision<=COALESCE(OLD.episode_mapping_revision,0) OR NEW.intent_version<=OLD.intent_version THEN RAISE EXCEPTION 'episode mapping binding requires newer revision and intent';END IF;
 END IF;RETURN NEW;END$$;
-- +goose StatementEnd
CREATE TRIGGER episode_mapping_binding BEFORE UPDATE ON subscription FOR EACH ROW EXECUTE FUNCTION pulse_episode_mapping_binding();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM episode_mapping_revision) THEN RAISE EXCEPTION 'episode mapping history cannot be discarded';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER episode_mapping_binding ON subscription;DROP FUNCTION pulse_episode_mapping_binding();ALTER TABLE subscription DROP CONSTRAINT episode_mapping_pointer,DROP COLUMN episode_mapping_revision;DROP TRIGGER episode_mapping_history ON episode_mapping_revision;DROP FUNCTION pulse_episode_mapping_history();DROP TABLE episode_mapping_revision;
