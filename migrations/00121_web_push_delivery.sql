-- +goose Up
CREATE TABLE web_push_delivery_binding (
 delivery_id UUID PRIMARY KEY REFERENCES outbox_delivery(id),
 device_id UUID NOT NULL REFERENCES web_push_device(id),
 user_id UUID NOT NULL REFERENCES app_user(id),
 vapid_version TEXT NOT NULL CHECK(vapid_version ~ '^[0-9a-f]{64}$'),
 unit_id UUID NOT NULL REFERENCES media_unit(id),
 file_id UUID NOT NULL REFERENCES library_file(id),
 root_id UUID NOT NULL REFERENCES library_root(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX web_push_delivery_device_idx ON web_push_delivery_binding(device_id,delivery_id);
-- +goose StatementBegin
CREATE FUNCTION protect_web_push_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'web push delivery destination is immutable' USING ERRCODE = '55000'; END $$;
-- +goose StatementEnd
CREATE TRIGGER protect_web_push_delivery BEFORE UPDATE OR DELETE ON web_push_delivery_binding FOR EACH ROW EXECUTE FUNCTION protect_web_push_delivery();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM web_push_delivery_binding) THEN
  RAISE EXCEPTION 'cannot discard web push delivery history' USING ERRCODE='55000';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE web_push_delivery_binding;
DROP FUNCTION protect_web_push_delivery();
