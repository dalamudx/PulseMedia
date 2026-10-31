-- name: LockAccountForSecurity :one
-- TOTP counters, recovery codes and credential mutations share an exclusive user lock.
SELECT * FROM app_user WHERE id = $1 FOR UPDATE;

-- name: LockAllAccountsForManagement :many
SELECT id FROM app_user ORDER BY id FOR UPDATE;

-- name: LockSecuritySession :one
SELECT id FROM app_session WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() FOR UPDATE;

-- name: ListManagedAccounts :many
SELECT * FROM app_user ORDER BY created_at,id;

-- name: UpdateManagedAccount :one
UPDATE app_user SET role=$2,is_active=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING *;

-- name: CountActiveAdministrators :one
SELECT count(*) FROM app_user WHERE is_active AND role='admin';

-- name: ListManagedSessions :many
SELECT * FROM app_session WHERE user_id=$1 AND expires_at>clock_timestamp() ORDER BY created_at,id;

-- name: RevokeManagedSession :execrows
DELETE FROM app_session WHERE id=$1 AND user_id=$2;

-- name: ChangeManagedPassword :exec
UPDATE app_user SET password_hash=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: DeleteOtherManagedSessions :exec
DELETE FROM app_session WHERE user_id=sqlc.arg(user_id) AND id<>sqlc.arg(keep_session_id)::uuid;

-- name: RevokeManagedAPITokens :exec
UPDATE api_token SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL;

-- name: ReadAccountSecurityCommand :one
SELECT * FROM account_security_command WHERE owner_id=$1 AND command_key=$2;

-- name: InsertAccountSecurityCommand :exec
INSERT INTO account_security_command(owner_id,command_key,request_digest,action,target_id,response) VALUES($1,$2,$3,$4,$5,$6);

-- name: LoadAccountTOTP :one
SELECT * FROM account_totp WHERE user_id=$1;

-- name: UpsertAccountTOTP :exec
INSERT INTO account_totp(user_id,enabled,secret_ciphertext,last_counter,challenge_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT(user_id) DO UPDATE SET enabled=EXCLUDED.enabled,secret_ciphertext=EXCLUDED.secret_ciphertext,last_counter=EXCLUDED.last_counter,challenge_id=EXCLUDED.challenge_id,expires_at=EXCLUDED.expires_at,updated_at=clock_timestamp();

-- name: ConsumeAccountTOTPRecovery :execrows
UPDATE account_totp_recovery SET used_at=clock_timestamp() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL;

-- name: DeleteAccountTOTPRecoveryCodes :exec
DELETE FROM account_totp_recovery WHERE user_id=$1;

-- name: InsertAccountTOTPRecoveryCode :exec
INSERT INTO account_totp_recovery(user_id,code_hash) VALUES($1,$2);

-- name: ReadAccountTOTPStatus :one
SELECT COALESCE((SELECT enabled FROM account_totp WHERE user_id=sqlc.arg(user_id)::uuid),false)::boolean AS enabled,
(SELECT count(*) FROM account_totp_recovery WHERE user_id=sqlc.arg(user_id)::uuid AND used_at IS NULL)::bigint AS recovery_codes_remaining;

-- name: DeleteAccountTOTP :exec
DELETE FROM account_totp WHERE user_id=$1;
