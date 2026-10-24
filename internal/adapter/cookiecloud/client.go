package cookiecloud

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type CookieItem = ports.CookieCloudCookie

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{httpClient: httpClient}
}

// DeriveKey returns the upstream passphrase, not a CBC key/IV pair.
func DeriveKey(userUUID, password string) []byte {
	raw := fmt.Sprintf("%s-%s", userUUID, password)
	hash := md5.Sum([]byte(raw))
	hexStr := hex.EncodeToString(hash[:])
	return []byte(hexStr[:16])
}

// DecryptPayload decrypts a raw OpenSSL Salted__ envelope using the derived
// CookieCloud passphrase. It never accepts the prototype's key-as-IV format.
func DecryptPayload(ciphertext, key []byte) ([]byte, error) {
	return decryptLegacy(ciphertext, key)
}

// SyncCookies is a compatibility view; production binding uses scoped ReadBackup records.
func (c *Client) SyncCookies(ctx context.Context, serverURL, userUUID, password string) (map[string]string, error) {
	backup, err := c.ReadBackup(ctx, serverURL, userUUID, password)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(backup.Cookies))
	for domain, items := range backup.Cookies {
		header := ""
		names := map[string]bool{}
		for _, item := range items {
			if names[item.Name] {
				return nil, ports.ErrBadResponse
			}
			names[item.Name] = true
			if header != "" {
				header += "; "
			}
			header += item.Name + "=" + item.Value
		}
		result[domain] = header
	}
	return result, nil
}
