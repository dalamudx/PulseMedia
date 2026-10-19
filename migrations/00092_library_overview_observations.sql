-- +goose Up
-- Read-only display observations. Neither table grants business authorization.
-- Separate tables preserve all existing frozen root/media row representations.
CREATE TABLE root_capacity_observation (
 scan_run_id UUID PRIMARY KEY REFERENCES scan_run(id) ON DELETE CASCADE,
 total_bytes BIGINT NOT NULL CHECK(total_bytes > 0),
 available_bytes BIGINT NOT NULL CHECK(available_bytes >= 0 AND available_bytes <= total_bytes),
 sampled_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX scan_run_overview_latest ON scan_run(root_id,created_at DESC,id DESC);

CREATE TABLE media_artwork (
 media_id UUID PRIMARY KEY REFERENCES media(id) ON DELETE CASCADE,
 provider TEXT NOT NULL CHECK(provider='tmdb'),
 namespace TEXT NOT NULL CHECK(namespace IN ('tmdb.movie','tmdb.tv')),
 external_id TEXT NOT NULL CHECK(external_id ~ '^[1-9][0-9]{0,18}$'),
 metadata_version BIGINT NOT NULL CHECK(metadata_version > 0),
 poster_path TEXT NOT NULL CHECK(poster_path ~ '^/[A-Za-z0-9_-]+\.(jpg|jpeg|png|webp)$'),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- +goose Down
-- Only derived display observations are removed; no business history is rewritten.
DROP TABLE media_artwork;
DROP INDEX scan_run_overview_latest;
DROP TABLE root_capacity_observation;
