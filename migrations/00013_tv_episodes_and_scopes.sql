-- +goose Up
-- M3-01: TV Seasons, Episodes, Air Dates, Scope Evolution & Specials

ALTER TABLE media_unit
ADD COLUMN IF NOT EXISTS season_number INT,
ADD COLUMN IF NOT EXISTS episode_number INT,
ADD COLUMN IF NOT EXISTS absolute_number INT;

CREATE INDEX IF NOT EXISTS media_unit_season_ep_idx
ON media_unit (media_id, season_number, episode_number);

ALTER TABLE subscription
ADD COLUMN IF NOT EXISTS include_specials BOOLEAN NOT NULL DEFAULT false,
ADD COLUMN IF NOT EXISTS seasons JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE subscription
DROP COLUMN IF EXISTS seasons,
DROP COLUMN IF EXISTS include_specials;

DROP INDEX IF EXISTS media_unit_season_ep_idx;

ALTER TABLE media_unit
DROP COLUMN IF EXISTS absolute_number,
DROP COLUMN IF EXISTS episode_number,
DROP COLUMN IF EXISTS season_number;
