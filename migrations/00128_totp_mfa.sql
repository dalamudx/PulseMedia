-- +goose Up
CREATE TABLE account_totp (
 user_id UUID PRIMARY KEY REFERENCES app_user(id),
 enabled BOOLEAN NOT NULL DEFAULT false,
 secret_ciphertext BYTEA NOT NULL CHECK (octet_length(secret_ciphertext)>0),
 last_counter BIGINT NOT NULL DEFAULT -1 CHECK(last_counter>=-1),
 challenge_id UUID NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE account_totp_recovery (
 user_id UUID NOT NULL REFERENCES account_totp(user_id) ON DELETE CASCADE,
 code_hash BYTEA NOT NULL CHECK (octet_length(code_hash)=32),
 used_at TIMESTAMPTZ,
 PRIMARY KEY(user_id,code_hash)
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM account_totp WHERE enabled) THEN RAISE EXCEPTION 'disable MFA before removing its authentication contract' USING ERRCODE='55000'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE account_totp_recovery;
DROP TABLE account_totp;
