package backup

import (
	"context"
	"encoding/base64"
	"errors"
	cryptoplatform "github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/jackc/pgx/v5"
)

// Validate native BYTEA ciphertext as bytes; only settings use base64 JSON.
func validateKey(ctx context.Context, db *pgx.Conn, key []byte) error {
	rows, e := db.Query(ctx, `SELECT api_key_encrypted FROM indexer UNION ALL SELECT link_encrypted FROM release_candidate UNION ALL SELECT password_encrypted FROM download_client UNION ALL SELECT api_key_encrypted FROM media_server UNION ALL SELECT config_encrypted FROM notification_channel UNION ALL SELECT rss_url_encrypted FROM indexer`)
	if e != nil {
		return errors.New("secret inventory unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return e
		}
		if b == nil {
			continue
		}
		if _, e = cryptoplatform.Decrypt(key, b); e != nil {
			return errors.New("source master key cannot decrypt stored credentials")
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	rows.Close()
	settings, e := db.Query(ctx, `SELECT value->>'ciphertext' FROM setting WHERE key='tmdb.api_key'`)
	if e != nil {
		return e
	}
	defer settings.Close()
	for settings.Next() {
		var value *string
		if e = settings.Scan(&value); e != nil {
			return e
		}
		if value == nil {
			return errors.New("invalid secret setting")
		}
		b, e := base64.StdEncoding.Strict().DecodeString(*value)
		if e != nil {
			return errors.New("invalid secret encoding")
		}
		if _, e = cryptoplatform.Decrypt(key, b); e != nil {
			return errors.New("source master key cannot decrypt settings")
		}
	}
	return settings.Err()
}
