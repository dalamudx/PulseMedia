-- +goose Up
-- Endpoints are derived exclusively from completed immutable retirement evidence.
CREATE VIEW library_verified_recycle AS
 SELECT 'movie:'||c.operation_id::text AS recycle_id,'movie'::text AS kind,
 (c.baseline->'old_file'->>'id')::uuid AS old_file_id,(c.baseline->'new_file'->>'id')::uuid AS replacement_file_id,
 (c.baseline->'root'->>'id')::uuid AS root_id,c.baseline->'old_file'->>'rel_path' AS original_rel,c.recycle_rel,r.retained_until,
 c.baseline->'preparation'->'snapshot'->'physical'->'old' AS identity,
 c.baseline->'preparation'->'snapshot'->'physical'->'root' AS root_identity,
 c.baseline->'root'->>'path' AS root_path,(c.baseline->'root'->>'generation')::bigint AS root_generation,
 jsonb_build_array(to_jsonb(c),to_jsonb(p),to_jsonb(r)) AS retirement_evidence
 FROM movie_upgrade_retirement_command c JOIN movie_upgrade_retirement_permit p USING(operation_id) JOIN movie_upgrade_retirement_result r USING(operation_id)
 WHERE r.status='retired' AND r.permit_id=p.permit_id AND r.baseline=c.baseline AND r.retained_until IS NOT NULL
 AND c.recycle_rel='.pulsemedia_recycle/'||c.operation_id::text||'/'||(c.baseline->'old_file'->>'rel_path')
 UNION ALL
 SELECT 'tv:'||a.operation_id::text,'tv',a.old_file_id,(phase.evidence->'plan'->'ingestion'->>'FileID')::uuid,
 a.root_id,phase.evidence->'plan'->'ingestion'->'plan'->'physical'->>'old_rel',phase.evidence->'plan'->>'recycle_rel',
 (phase.evidence->>'retained_until')::timestamptz,
 phase.evidence->'plan'->'ingestion'->'plan'->'physical'->'old',phase.evidence->'plan'->'ingestion'->'plan'->'physical'->'root',
 phase.evidence->'plan'->'ingestion'->'plan'->'physical'->>'root_path',
 NULL::bigint,
 jsonb_build_array(to_jsonb(a),to_jsonb(p),to_jsonb(ret),to_jsonb(phase))
 FROM tv_episode_upgrade_authority a JOIN tv_episode_upgrade_phase phase ON phase.operation_id=a.operation_id AND phase.action='retire'
 JOIN tv_episode_upgrade_permit p ON p.operation_id=a.operation_id AND p.action='retire'
 JOIN tv_episode_upgrade_return ret ON ret.operation_id=a.operation_id AND ret.action='retire'
 WHERE ret.status='confirmed' AND ret.owner=p.owner AND ret.token=p.token AND ret.evidence=phase.evidence
 AND phase.result->>'status'='retired' AND phase.evidence->>'retained_until' IS NOT NULL;
CREATE TABLE library_recycle_observation (
 id UUID PRIMARY KEY,recycle_id TEXT NOT NULL,actor_id UUID NOT NULL REFERENCES app_user(id),
 identity JSONB NOT NULL,allocated_bytes BIGINT NOT NULL CHECK(allocated_bytes>=0),link_count BIGINT NOT NULL CHECK(link_count>0),observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX library_recycle_observation_latest ON library_recycle_observation(recycle_id,observed_at DESC,id);
-- +goose StatementBegin
CREATE FUNCTION pulse_library_recycle_observation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'recycle observations are immutable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM app_user WHERE id=NEW.actor_id AND is_active AND role='admin') OR NOT EXISTS(SELECT 1 FROM library_verified_recycle WHERE recycle_id=NEW.recycle_id AND identity=NEW.identity) THEN RAISE EXCEPTION 'verified recycle evidence required'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER library_recycle_observation_insert BEFORE INSERT ON library_recycle_observation FOR EACH ROW EXECUTE FUNCTION pulse_library_recycle_observation_guard();
CREATE TRIGGER library_recycle_observation_immutable BEFORE UPDATE OR DELETE ON library_recycle_observation FOR EACH ROW EXECUTE FUNCTION pulse_library_recycle_observation_guard();
CREATE TRIGGER library_recycle_observation_truncate BEFORE TRUNCATE ON library_recycle_observation FOR EACH STATEMENT EXECUTE FUNCTION pulse_library_recycle_observation_guard();
-- +goose Down
LOCK TABLE library_recycle_observation IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM library_recycle_observation) OR EXISTS(SELECT 1 FROM library_maintenance_command WHERE action IN ('restore','cleanup')) THEN RAISE EXCEPTION 'recycle maintenance history cannot be discarded'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE library_recycle_observation;
DROP FUNCTION pulse_library_recycle_observation_guard();
DROP VIEW library_verified_recycle;
