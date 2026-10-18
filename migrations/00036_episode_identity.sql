-- +goose Up
CREATE TABLE media_unit_identity(
 unit_id UUID NOT NULL REFERENCES media_unit(id), namespace TEXT NOT NULL CHECK(namespace='tmdb.episode'), external_id BIGINT NOT NULL CHECK(external_id>0), tmdb_series_id BIGINT NOT NULL CHECK(tmdb_series_id>0), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(namespace,external_id), UNIQUE(unit_id,namespace));
-- +goose StatementBegin
CREATE FUNCTION pulse_episode_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'episode identity history is immutable';END IF;
 IF NOT EXISTS(SELECT 1 FROM media_unit u JOIN media m ON m.id=u.media_id JOIN media_identity i ON i.media_id=m.id WHERE u.id=NEW.unit_id AND m.media_type='tv' AND i.namespace='tmdb.tv' AND i.external_id=NEW.tmdb_series_id::text) THEN RAISE EXCEPTION 'episode parent identity mismatch';END IF;
 RETURN NEW;END$$;
-- +goose StatementEnd
CREATE TRIGGER episode_identity_guard BEFORE INSERT OR UPDATE OR DELETE ON media_unit_identity FOR EACH ROW EXECUTE FUNCTION pulse_episode_identity_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_episode_identity_revision() RETURNS trigger LANGUAGE plpgsql AS $$DECLARE target UUID;BEGIN
 FOR target IN SELECT DISTINCT unit_id FROM new_rows ORDER BY unit_id LOOP UPDATE media_unit SET metadata_version=metadata_version+1,updated_at=clock_timestamp() WHERE id=target;END LOOP;RETURN NULL;END$$;
-- +goose StatementEnd
CREATE TRIGGER episode_identity_revision AFTER INSERT ON media_unit_identity REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_episode_identity_revision();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM media_unit_identity) THEN RAISE EXCEPTION 'episode identity evidence cannot be discarded';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER episode_identity_revision ON media_unit_identity;DROP FUNCTION pulse_episode_identity_revision();DROP TRIGGER episode_identity_guard ON media_unit_identity;DROP FUNCTION pulse_episode_identity_guard();DROP TABLE media_unit_identity;
