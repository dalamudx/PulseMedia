-- +goose Up
-- Legacy plaintext cannot be re-encrypted in SQL without exposing the master key.
-- Administrator must configure TMDB again after upgrade.
DELETE FROM setting WHERE key = 'tmdb.api_key' AND NOT (value ? 'ciphertext');
UPDATE audit_log SET payload = '{"redacted":true}'::jsonb
 WHERE action = 'setting.update' AND target_id = 'tmdb.api_key';
UPDATE idempotency_record
 SET response_body = jsonb_set(response_body, '{value}', '{"configured":false}'::jsonb, true)
 WHERE response_body->>'key' = 'tmdb.api_key';

-- +goose Down
-- Intentionally irreversible: erased values must never be restored.
SELECT 1;
