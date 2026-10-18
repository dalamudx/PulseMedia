-- +goose Up
-- Legacy demands cannot prove current control/source provenance; do not backfill
-- fabricated authority. Existing submitting/unknown tasks stay observable.
ALTER TABLE download_requirement ADD COLUMN submission_fence JSONB;
ALTER TABLE download_requirement ADD COLUMN released_at TIMESTAMPTZ;
ALTER TABLE download_requirement DROP CONSTRAINT download_requirement_state_check;
ALTER TABLE download_requirement ADD CONSTRAINT download_requirement_state_check CHECK(state IN ('pending','imported','released'));
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM download_requirement WHERE state='released') THEN RAISE EXCEPTION 'cannot downgrade released requirement history'; END IF; END $$;
-- +goose StatementEnd
ALTER TABLE download_requirement DROP CONSTRAINT download_requirement_state_check;
ALTER TABLE download_requirement ADD CONSTRAINT download_requirement_state_check CHECK(state IN ('pending','imported'));
ALTER TABLE download_requirement DROP COLUMN submission_fence,DROP COLUMN released_at;
