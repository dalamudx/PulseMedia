-- +goose Up
-- Independent cross-source evidence; historical TMDB identity and receipt shapes stay unchanged.
CREATE TABLE tvdb_series_identity (
 media_id UUID PRIMARY KEY REFERENCES media(id),
 tmdb_series_id BIGINT NOT NULL UNIQUE CHECK(tmdb_series_id>0),
 tvdb_series_id BIGINT NOT NULL UNIQUE CHECK(tvdb_series_id>0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(media_id,tmdb_series_id,tvdb_series_id)
);
CREATE TABLE tvdb_episode_evidence (
 unit_id UUID PRIMARY KEY REFERENCES media_unit(id),
 media_id UUID NOT NULL,
 tmdb_series_id BIGINT NOT NULL CHECK(tmdb_series_id>0),
 tvdb_series_id BIGINT NOT NULL CHECK(tvdb_series_id>0),
 tmdb_episode_id BIGINT NOT NULL UNIQUE CHECK(tmdb_episode_id>0),
 tvdb_episode_id BIGINT NOT NULL UNIQUE CHECK(tvdb_episode_id>0),
 season_number INTEGER NOT NULL CHECK(season_number BETWEEN 0 AND 999),
 episode_number INTEGER NOT NULL CHECK(episode_number BETWEEN 1 AND 9999),
 credential_version BIGINT NOT NULL CHECK(credential_version>0),
 fallback_version BIGINT NOT NULL CHECK(fallback_version>0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(media_id,tmdb_series_id,tvdb_series_id) REFERENCES tvdb_series_identity(media_id,tmdb_series_id,tvdb_series_id)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_tvdb_series_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'TVDB series identity history is immutable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM media m JOIN media_identity i ON i.media_id=m.id WHERE m.id=NEW.media_id AND m.media_type='tv' AND i.namespace='tmdb.tv' AND i.external_id=NEW.tmdb_series_id::text) THEN
  RAISE EXCEPTION 'TVDB series canonical identity mismatch';
 END IF;
 RETURN NEW;
END$$;
-- +goose StatementEnd
CREATE TRIGGER tvdb_series_identity_guard BEFORE INSERT OR UPDATE OR DELETE ON tvdb_series_identity FOR EACH ROW EXECUTE FUNCTION pulse_tvdb_series_identity_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_tvdb_episode_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'TVDB episode evidence is immutable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM media_unit u JOIN media_unit_identity i ON i.unit_id=u.id WHERE u.id=NEW.unit_id AND u.media_id=NEW.media_id AND NOT u.retired AND u.season_number=NEW.season_number AND u.episode_number=NEW.episode_number AND u.unit_key=format('S%sE%s',lpad(NEW.season_number::text,greatest(2,length(NEW.season_number::text)),'0'),lpad(NEW.episode_number::text,greatest(2,length(NEW.episode_number::text)),'0')) AND i.namespace='tmdb.episode' AND i.external_id=NEW.tmdb_episode_id AND i.tmdb_series_id=NEW.tmdb_series_id) THEN
  RAISE EXCEPTION 'TVDB episode canonical identity mismatch';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM setting WHERE key='tvdb.credentials' AND version=NEW.credential_version AND jsonb_typeof(value->'ciphertext')='string' AND length(value->>'ciphertext')>0) OR NOT EXISTS(SELECT 1 FROM setting WHERE key='tvdb.fallback' AND version=NEW.fallback_version AND value='{"enabled":true}'::jsonb) THEN
  RAISE EXCEPTION 'TVDB episode configuration changed';
 END IF;
 RETURN NEW;
END$$;
-- +goose StatementEnd
CREATE TRIGGER tvdb_episode_evidence_guard BEFORE INSERT OR UPDATE OR DELETE ON tvdb_episode_evidence FOR EACH ROW EXECUTE FUNCTION pulse_tvdb_episode_evidence_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_tvdb_unit_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF EXISTS(SELECT 1 FROM tvdb_episode_evidence WHERE unit_id=OLD.id) AND (NEW.media_id<>OLD.media_id OR NEW.unit_key<>OLD.unit_key OR NEW.season_number IS DISTINCT FROM OLD.season_number OR NEW.episode_number IS DISTINCT FROM OLD.episode_number OR (NEW.retired AND NOT OLD.retired)) THEN
  RAISE EXCEPTION 'TVDB supplemented unit requires explicit identity review';
 END IF;
 RETURN NEW;
END$$;
-- +goose StatementEnd
CREATE TRIGGER tvdb_unit_identity_guard BEFORE UPDATE ON media_unit FOR EACH ROW EXECUTE FUNCTION pulse_tvdb_unit_identity_guard();

-- +goose Down
LOCK TABLE tvdb_series_identity,tvdb_episode_evidence IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM tvdb_series_identity) OR EXISTS(SELECT 1 FROM tvdb_episode_evidence) THEN RAISE EXCEPTION 'TVDB metadata evidence cannot be discarded'; END IF;
END$$;
-- +goose StatementEnd
DROP TRIGGER tvdb_unit_identity_guard ON media_unit;
DROP FUNCTION pulse_tvdb_unit_identity_guard();
DROP TABLE tvdb_episode_evidence;
DROP TABLE tvdb_series_identity;
DROP FUNCTION pulse_tvdb_episode_evidence_guard();
DROP FUNCTION pulse_tvdb_series_identity_guard();
