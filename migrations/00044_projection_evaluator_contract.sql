-- +goose Up
-- Cached decisions depend on evaluator semantics as well as source versions.
-- Old rows retain their facts and outcome for history, but lack current authority.
ALTER TABLE wanted_item ADD COLUMN evaluated_contract_version TEXT,
 ADD COLUMN evaluated_write_token UUID;

-- +goose StatementBegin
CREATE FUNCTION pulse_projection_contract_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.evaluated_write_token IS NOT DISTINCT FROM OLD.evaluated_write_token THEN
  NEW.evaluated_contract_version := NULL;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- An old binary cannot retain authority previously written by the new projector.
-- Merely changing a cached result (even to the same value) must carry a fresh token.
CREATE TRIGGER projection_contract_guard
 BEFORE UPDATE OF is_satisfied,current_fact_stage,last_evaluated_at ON wanted_item
 FOR EACH ROW EXECUTE FUNCTION pulse_projection_contract_guard();

CREATE OR REPLACE VIEW wanted_projection_validity AS
SELECT w.id,
 (w.evaluated_contract_version='ownership.quality.v1'
 AND w.evaluated_write_token IS NOT NULL
 AND w.evaluated_intent_version IS NOT DISTINCT FROM s.intent_version
 AND w.evaluated_policy_id IS NOT DISTINCT FROM s.quality_policy_id
 AND w.evaluated_policy_revision IS NOT DISTINCT FROM s.quality_policy_revision
 AND w.evaluated_unit_id IS NOT DISTINCT FROM w.unit_id
 AND w.evaluated_media_id IS NOT DISTINCT FROM s.media_id
 AND u.media_id=s.media_id
 AND w.evaluated_root_id IS NOT DISTINCT FROM s.root_id
 AND w.evaluated_root_generation IS NOT DISTINCT FROM r.generation
 AND w.evaluated_health_revision IS NOT DISTINCT FROM r.health_revision
 AND w.evaluated_media_metadata_version IS NOT DISTINCT FROM m.metadata_version
 AND w.evaluated_unit_metadata_version IS NOT DISTINCT FROM u.metadata_version
 AND w.evaluated_facts_version IS NOT DISTINCT FROM u.facts_version
 AND w.evaluated_state IS NOT DISTINCT FROM s.state
 AND w.evaluated_skipped IS NOT DISTINCT FROM w.is_skipped) IS TRUE AS inputs_current
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
JOIN library_root r ON r.id=s.root_id JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.id=w.unit_id;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wanted_item WHERE evaluated_write_token IS NOT NULL) THEN
  RAISE EXCEPTION 'cannot discard projection evaluator evidence';
 END IF;
END $$;
-- +goose StatementEnd
CREATE OR REPLACE VIEW wanted_projection_validity AS
SELECT w.id,
 (w.evaluated_intent_version IS NOT DISTINCT FROM s.intent_version
 AND w.evaluated_policy_id IS NOT DISTINCT FROM s.quality_policy_id
 AND w.evaluated_policy_revision IS NOT DISTINCT FROM s.quality_policy_revision
 AND w.evaluated_unit_id IS NOT DISTINCT FROM w.unit_id
 AND w.evaluated_media_id IS NOT DISTINCT FROM s.media_id
 AND u.media_id=s.media_id
 AND w.evaluated_root_id IS NOT DISTINCT FROM s.root_id
 AND w.evaluated_root_generation IS NOT DISTINCT FROM r.generation
 AND w.evaluated_health_revision IS NOT DISTINCT FROM r.health_revision
 AND w.evaluated_media_metadata_version IS NOT DISTINCT FROM m.metadata_version
 AND w.evaluated_unit_metadata_version IS NOT DISTINCT FROM u.metadata_version
 AND w.evaluated_facts_version IS NOT DISTINCT FROM u.facts_version
 AND w.evaluated_state IS NOT DISTINCT FROM s.state
 AND w.evaluated_skipped IS NOT DISTINCT FROM w.is_skipped) AS inputs_current
FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
JOIN library_root r ON r.id=s.root_id JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.id=w.unit_id;
DROP TRIGGER projection_contract_guard ON wanted_item;
DROP FUNCTION pulse_projection_contract_guard();
ALTER TABLE wanted_item DROP COLUMN evaluated_contract_version,DROP COLUMN evaluated_write_token;
