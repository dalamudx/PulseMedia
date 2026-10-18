-- +goose Up
-- M1-06: Atomic Unit Reservation, Download Requirements & Acquisition UoW

CREATE TABLE IF NOT EXISTS unit_reservation (
    id UUID PRIMARY KEY,
    unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE CASCADE,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
    purpose TEXT NOT NULL,
    candidate_id UUID NOT NULL REFERENCES release_candidate(id) ON DELETE RESTRICT,
    operation_id UUID NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('reserved', 'dispatched', 'released', 'consumed')),
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- Invariant: Only one active reservation allowed per (unit_id, root_id, purpose)
CREATE UNIQUE INDEX IF NOT EXISTS unit_reservation_active_uniq
ON unit_reservation (unit_id, root_id, purpose)
WHERE state IN ('reserved', 'dispatched');

CREATE INDEX IF NOT EXISTS unit_reservation_op_idx ON unit_reservation (operation_id);

CREATE TABLE IF NOT EXISTS download_requirement (
    id UUID PRIMARY KEY,
    operation_id UUID NOT NULL,
    unit_id UUID NOT NULL REFERENCES media_unit(id) ON DELETE CASCADE,
    root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT download_requirement_op_unit_root_uniq UNIQUE (operation_id, unit_id, root_id, actor_type, actor_id)
);

CREATE INDEX IF NOT EXISTS download_requirement_unit_idx ON download_requirement (unit_id);

-- +goose Down
DROP TABLE IF EXISTS download_requirement;
DROP TABLE IF EXISTS unit_reservation;
