package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWebPushDeploymentConfiguration(t *testing.T) {
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"PULSE_WEB_PUSH_ENABLED": "true", "PULSE_WEB_PUSH_PRIVATE_KEY": base64.RawURLEncoding.EncodeToString(private.Bytes()), "PULSE_WEB_PUSH_PUBLIC_KEY": base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), "PULSE_WEB_PUSH_SUBJECT": "mailto:push@example.org"}
	load := func() (*Config, error) {
		return Load(WithEnvLookup(func(key string) (string, bool) { v, ok := values[key]; return v, ok }))
	}
	c, err := load()
	if err != nil || !c.WebPush.Enabled || c.WebPush.PublicKey != values["PULSE_WEB_PUSH_PUBLIC_KEY"] {
		t.Fatal("valid config rejected", err)
	}
	raw, err := json.Marshal(c.WebPush)
	if err != nil || strings.Contains(string(raw), values["PULSE_WEB_PUSH_PRIVATE_KEY"]) {
		t.Fatal("VAPID private key in JSON", err)
	}
	for _, name := range []string{"PULSE_WEB_PUSH_ENABLED", "PULSE_WEB_PUSH_PRIVATE_KEY", "PULSE_WEB_PUSH_PUBLIC_KEY", "PULSE_WEB_PUSH_SUBJECT"} {
		t.Run(name, func(t *testing.T) {
			old := values[name]
			values[name] = "invalid-private-config"
			defer func() { values[name] = old }()
			_, err := load()
			if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "invalid-private-config") {
				t.Fatal("bad config escaped", err)
			}
		})
	}
	values["PULSE_WEB_PUSH_ENABLED"] = "false"
	values["PULSE_WEB_PUSH_PRIVATE_KEY"] = ""
	values["PULSE_WEB_PUSH_PUBLIC_KEY"] = ""
	if c, err = load(); err != nil || c.WebPush.Enabled {
		t.Fatal("offline disablement", err)
	}
}
