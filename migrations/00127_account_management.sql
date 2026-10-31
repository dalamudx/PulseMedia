-- +goose Up
-- Dedicated receipts contain safe response metadata only, never credentials.
CREATE TABLE account_security_command (
 owner_id UUID NOT NULL REFERENCES app_user(id),
 command_key TEXT NOT NULL CHECK (length(command_key) BETWEEN 1 AND 160),
 request_digest TEXT NOT NULL CHECK (length(request_digest)=64),
 action TEXT NOT NULL,
 target_id UUID NOT NULL,
 response JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(owner_id,command_key)
);

-- +goose Down
DROP TABLE account_security_command;
