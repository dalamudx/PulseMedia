-- +goose Up
-- Quality facts signed before competing unsupported formats were rejected
-- cannot keep a current green projection. History, facts, physical leases and
-- placeholders are preserved; the scheduler recomputes through the new contract.
CREATE OR REPLACE VIEW wanted_projection_validity AS
SELECT w.id,
 (w.evaluated_contract_version='ownership.quality.v2'
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
  RAISE EXCEPTION 'cannot revive superseded quality evaluator evidence';
 END IF;
END $$;
-- +goose StatementEnd
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
