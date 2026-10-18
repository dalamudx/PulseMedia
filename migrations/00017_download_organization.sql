-- +goose Up
-- A completed download is not imported. Its local organization is separately durable.
ALTER TABLE download_task ADD COLUMN organization_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (organization_state IN ('pending','queued','review_required','completed'));
ALTER TABLE download_task ADD COLUMN organization_reason TEXT;
ALTER TABLE download_task ADD COLUMN transfer_plan_id UUID REFERENCES transfer_plan(id) ON DELETE RESTRICT;
ALTER TABLE download_requirement ADD COLUMN state TEXT NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending','imported'));
-- No new execution evidence can be inferred for legacy terminal downloads.
UPDATE download_task SET organization_state='review_required', organization_reason='legacy completion requires review'
WHERE state='completed';

-- +goose Down
ALTER TABLE download_requirement DROP COLUMN state;
ALTER TABLE download_task DROP COLUMN transfer_plan_id;
ALTER TABLE download_task DROP COLUMN organization_reason;
ALTER TABLE download_task DROP COLUMN organization_state;
