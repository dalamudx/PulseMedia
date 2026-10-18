-- +goose Up
-- M1-08: Transfer Plans, Steps, Checkpoints & Inode Post-Conditions

CREATE TABLE IF NOT EXISTS transfer_plan (
    id UUID PRIMARY KEY,
    operation_id UUID NOT NULL,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('created', 'approved', 'executing', 'completed', 'failed', 'review_required')),
    requires_approval BOOLEAN NOT NULL DEFAULT false,
    strategy TEXT NOT NULL CHECK (strategy IN ('hardlink', 'copy', 'reflink', 'move')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS transfer_plan_op_idx ON transfer_plan (operation_id);

CREATE TABLE IF NOT EXISTS transfer_step (
    id UUID PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES transfer_plan(id) ON DELETE CASCADE,
    unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE RESTRICT,
    source_path TEXT NOT NULL,
    target_path TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('hardlink', 'copy', 'reflink', 'move')),
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    step_order INT NOT NULL,
    source_size BIGINT NOT NULL,
    source_mtime BIGINT NOT NULL,
    target_file_id UUID REFERENCES library_file(id) ON DELETE SET NULL,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS transfer_step_plan_order_idx ON transfer_step (plan_id, step_order);

-- +goose Down
DROP TABLE IF EXISTS transfer_step;
DROP TABLE IF EXISTS transfer_plan;
