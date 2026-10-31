-- +goose Up
CREATE INDEX search_round_subscription_diagnostics ON search_round(subscription_id,created_at DESC,id DESC);
CREATE INDEX automatic_tv_round_subscription_diagnostics ON automatic_tv_round(subscription_id,created_at DESC,id DESC);
-- +goose Down
DROP INDEX automatic_tv_round_subscription_diagnostics;
DROP INDEX search_round_subscription_diagnostics;
