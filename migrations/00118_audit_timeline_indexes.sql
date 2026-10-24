-- +goose Up
CREATE INDEX audit_log_timeline_idx ON audit_log (created_at DESC, id DESC);
CREATE INDEX audit_log_actor_timeline_idx ON audit_log (actor_id, created_at DESC, id DESC);
CREATE INDEX audit_log_action_timeline_idx ON audit_log (action, created_at DESC, id DESC);
CREATE INDEX audit_log_target_timeline_idx ON audit_log (target_type, target_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX audit_log_target_timeline_idx;
DROP INDEX audit_log_action_timeline_idx;
DROP INDEX audit_log_actor_timeline_idx;
DROP INDEX audit_log_timeline_idx;
