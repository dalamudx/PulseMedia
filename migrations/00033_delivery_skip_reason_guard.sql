-- +goose Up
-- SQL CHECK accepts unknown, so the audited reason must be explicitly non-null.
ALTER TABLE outbox_delivery ADD CONSTRAINT delivery_skip_reason_required CHECK(state<>'skipped' OR skip_reason IS NOT NULL);
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM outbox_delivery WHERE state='skipped') THEN RAISE EXCEPTION 'audited skip reason cannot be weakened with history';END IF;END$$;
-- +goose StatementEnd
ALTER TABLE outbox_delivery DROP CONSTRAINT delivery_skip_reason_required;
