package webpush

import (
	"crypto/ecdh"
	"encoding/base64"
	"net/url"
	"strings"
)

// Subscription contains browser-generated private delivery capabilities.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func ValidSubscription(s Subscription) bool {
	if len(s.Endpoint) > 4096 {
		return false
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Path == "" || u.Path == "/" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := u.Hostname()
	if host != strings.ToLower(host) {
		return false
	}
	switch host {
	case "fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com":
	default:
		if !strings.HasSuffix(host, ".notify.windows.com") || len(host) <= len(".notify.windows.com") {
			return false
		}
	}
	key, ok := decode(s.P256DH, 65)
	if !ok {
		return false
	}
	if _, err = ecdh.P256().NewPublicKey(key); err != nil {
		return false
	}
	_, ok = decode(s.Auth, 16)
	return ok
}

func ValidPublicKey(value string) bool {
	key, ok := decode(value, 65)
	if !ok {
		return false
	}
	_, err := ecdh.P256().NewPublicKey(key)
	return err == nil
}

func decode(value string, size int) ([]byte, bool) {
	if len(value) != base64.RawURLEncoding.EncodedLen(size) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return raw, err == nil && len(raw) == size
}
