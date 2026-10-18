-- +goose Up
CREATE TABLE tv_season_metadata_snapshot (
 media_id UUID NOT NULL REFERENCES media(id) ON DELETE RESTRICT,
 media_version BIGINT NOT NULL CHECK (media_version > 0),
 season_number INTEGER NOT NULL CHECK (season_number BETWEEN 1 AND 999),
 source_tmdb_id BIGINT NOT NULL CHECK (source_tmdb_id > 0),
 units JSONB NOT NULL CHECK (jsonb_typeof(units) = 'array' AND jsonb_array_length(units) BETWEEN 1 AND 1000),
 content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (media_id, media_version, season_number)
);

-- +goose Down
-- +goose StatementBegin
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM tv_season_metadata_snapshot) THEN RAISE EXCEPTION 'complete season metadata history prevents downgrade'; END IF;
END$$;
-- +goose StatementEnd
DROP TABLE tv_season_metadata_snapshot;
