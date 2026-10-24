-- +goose Up
ALTER TABLE download_client DROP CONSTRAINT download_client_client_type_check;
ALTER TABLE download_client ADD CONSTRAINT download_client_client_type_check
    CHECK (client_type IN ('qbittorrent', 'transmission', 'rtorrent'));

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM download_client WHERE client_type = 'rtorrent') THEN
        RAISE EXCEPTION 'cannot roll back while rTorrent download clients remain' USING ERRCODE = '55000';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE download_client DROP CONSTRAINT download_client_client_type_check;
ALTER TABLE download_client ADD CONSTRAINT download_client_client_type_check
    CHECK (client_type IN ('qbittorrent', 'transmission'));
