-- +goose Up
ALTER TABLE outbox_delivery DROP CONSTRAINT outbox_delivery_state_check;
ALTER TABLE outbox_delivery ADD CONSTRAINT outbox_delivery_state_check CHECK(state IN ('pending','delivering','delivered','failed','dead','skipped'));
ALTER TABLE outbox_delivery ADD COLUMN skipped_at TIMESTAMPTZ,ADD COLUMN skip_reason TEXT;
ALTER TABLE outbox_delivery ADD CONSTRAINT delivery_skip_consistency CHECK((state='skipped' AND skipped_at IS NOT NULL AND skip_reason IN ('handled_elsewhere','recipient_retired','event_obsolete') AND delivered_at IS NULL AND lease_owner IS NULL AND lease_until IS NULL) OR(state<>'skipped' AND skipped_at IS NULL AND skip_reason IS NULL));
-- +goose StatementBegin
CREATE FUNCTION pulse_delivery_skip_guard() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF OLD.state='skipped' THEN RAISE EXCEPTION 'audited delivery skip is immutable';END IF;IF TG_OP='DELETE' THEN RETURN OLD;END IF;RETURN NEW;END$$;
-- +goose StatementEnd
CREATE TRIGGER delivery_skip_guard BEFORE UPDATE OR DELETE ON outbox_delivery FOR EACH ROW EXECUTE FUNCTION pulse_delivery_skip_guard();
-- +goose Down
-- +goose StatementBegin
DO $$BEGIN IF EXISTS(SELECT 1 FROM outbox_delivery WHERE state='skipped') THEN RAISE EXCEPTION 'audited skips cannot be discarded or replayed';END IF;END$$;
-- +goose StatementEnd
DROP TRIGGER delivery_skip_guard ON outbox_delivery;
DROP FUNCTION pulse_delivery_skip_guard();
ALTER TABLE outbox_delivery DROP CONSTRAINT delivery_skip_consistency,DROP COLUMN skipped_at,DROP COLUMN skip_reason;
ALTER TABLE outbox_delivery DROP CONSTRAINT outbox_delivery_state_check;
ALTER TABLE outbox_delivery ADD CONSTRAINT outbox_delivery_state_check CHECK(state IN ('pending','delivering','delivered','failed','dead'));
