-- +goose Up
ALTER TABLE indexer ADD COLUMN health_checked_at TIMESTAMPTZ,
 ADD COLUMN health_observed_at TIMESTAMPTZ,
 ADD COLUMN health_query_id UUID REFERENCES indexer_query(id) ON DELETE SET NULL,
 ADD COLUMN health_next_check_at TIMESTAMPTZ NOT NULL DEFAULT (clock_timestamp()+interval '15 minutes'),
 ADD COLUMN health_failures INTEGER NOT NULL DEFAULT 0 CHECK(health_failures>=0),
 ADD COLUMN health_reason TEXT NOT NULL DEFAULT 'not_checked' CHECK(health_reason IN ('not_checked','ok','unavailable','rate_limited'));
CREATE INDEX indexer_health_due_idx ON indexer(health_next_check_at,id);
-- +goose StatementBegin
CREATE FUNCTION pulse_indexer_health_schedule() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.config_version<>OLD.config_version THEN NEW.health_observed_at:=NULL;NEW.health_query_id:=NULL;NEW.health_checked_at:=NULL;NEW.health_reason:='not_checked';NEW.health_failures:=0;NEW.health_next_check_at:=clock_timestamp()+interval '30 seconds';
 ELSIF NEW.health<>OLD.health AND NEW.health<>'online' AND NEW.health_checked_at IS NOT DISTINCT FROM OLD.health_checked_at THEN NEW.health_next_check_at:=LEAST(NEW.health_next_check_at,clock_timestamp()+interval '30 seconds'); END IF;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER indexer_health_schedule BEFORE UPDATE ON indexer FOR EACH ROW EXECUTE FUNCTION pulse_indexer_health_schedule();
-- +goose Down
DROP TRIGGER indexer_health_schedule ON indexer;DROP FUNCTION pulse_indexer_health_schedule();
DROP INDEX indexer_health_due_idx;
ALTER TABLE indexer DROP COLUMN health_observed_at,DROP COLUMN health_query_id,DROP COLUMN health_checked_at,DROP COLUMN health_next_check_at,DROP COLUMN health_failures,DROP COLUMN health_reason;
