-- +goose Up
-- Legacy plans have no trustworthy frozen identity and must be reviewed.
ALTER TABLE transfer_plan ADD COLUMN execution_owner TEXT;
ALTER TABLE transfer_plan ADD COLUMN execution_token BIGINT NOT NULL DEFAULT 0;
ALTER TABLE transfer_plan ADD COLUMN lease_until TIMESTAMPTZ;
ALTER TABLE transfer_plan ADD COLUMN root_path TEXT;
ALTER TABLE transfer_plan ADD COLUMN root_generation BIGINT;
ALTER TABLE transfer_plan ADD COLUMN root_device BIGINT;
ALTER TABLE transfer_plan ADD COLUMN root_inode BIGINT;
ALTER TABLE transfer_plan ADD COLUMN old_file_id UUID REFERENCES library_file(id) ON DELETE RESTRICT;
ALTER TABLE transfer_step ADD COLUMN source_root TEXT;
ALTER TABLE transfer_step ADD COLUMN source_rel TEXT;
ALTER TABLE transfer_step ADD COLUMN target_rel TEXT;
ALTER TABLE transfer_step ADD COLUMN source_device BIGINT;
ALTER TABLE transfer_step ADD COLUMN source_inode BIGINT;
ALTER TABLE transfer_step ADD COLUMN quality_profile JSONB;

-- Old, non-terminal plans cannot be automatically approved after upgrading.
UPDATE transfer_plan SET state = 'review_required', updated_at = statement_timestamp()
WHERE state <> 'completed';

-- +goose Down
ALTER TABLE transfer_step DROP COLUMN quality_profile, DROP COLUMN source_inode, DROP COLUMN source_device, DROP COLUMN target_rel, DROP COLUMN source_rel, DROP COLUMN source_root;
ALTER TABLE transfer_plan DROP COLUMN old_file_id, DROP COLUMN root_inode, DROP COLUMN root_device, DROP COLUMN root_generation, DROP COLUMN root_path, DROP COLUMN lease_until, DROP COLUMN execution_token, DROP COLUMN execution_owner;
