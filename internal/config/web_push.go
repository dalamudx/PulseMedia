package config

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
)

// The private VAPID identity stays in deployment secrets and is never sent to a browser.
type WebPushConfig struct {
	Enabled    bool   `yaml:"enabled"`
	PublicKey  string `yaml:"public_key"`
	PrivateKey string `yaml:"private_key" json:"-"`
	Subject    string `yaml:"subject"`
}

func (c WebPushConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	private, err := base64.RawURLEncoding.Strict().DecodeString(c.PrivateKey)
	if err != nil || len(private) != 32 {
		return ErrInvalidConfig
	}
	key, err := ecdh.P256().NewPrivateKey(private)
	if err != nil {
		return ErrInvalidConfig
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(c.PublicKey)
	if err != nil || len(public) != 65 || !bytes.Equal(public, key.PublicKey().Bytes()) {
		return ErrInvalidConfig
	}
	if c.Subject == "" || len(c.Subject) > 256 || strings.TrimSpace(c.Subject) != c.Subject {
		return ErrInvalidConfig
	}
	if strings.HasPrefix(c.Subject, "https://") {
		u, err := url.Parse(c.Subject)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return ErrInvalidConfig
		}
		return nil
	}
	subject := strings.TrimPrefix(c.Subject, "mailto:")
	address, err := mail.ParseAddress(subject)
	if err != nil || address.Name != "" || address.Address != subject || !strings.Contains(subject, "@") {
		return ErrInvalidConfig
	}
	return nil
}
func (l *loader) loadWebPush(c *Config) error {
	if raw, ok := l.envLookup(l.envPrefix + "WEB_PUSH_ENABLED"); ok {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return ErrInvalidConfig
		}
		c.WebPush.Enabled = enabled
	}
	for name, dest := range map[string]*string{"WEB_PUSH_PUBLIC_KEY": &c.WebPush.PublicKey, "WEB_PUSH_PRIVATE_KEY": &c.WebPush.PrivateKey, "WEB_PUSH_SUBJECT": &c.WebPush.Subject} {
		if value, ok := l.envLookup(l.envPrefix + name); ok {
			*dest = value
		}
	}
	return nil
}
