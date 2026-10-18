package notify

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/url"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid notification channel configuration")
var ErrIdempotencyConflict = ports.ErrChannelCreateConflict

// All fields are secrets or private integration destinations; store as ciphertext only.
type ChannelConfig = ports.NotificationChannelConfig
type ClientFactory func(string, ChannelConfig) (ports.Notifier, error)

func (d *Dispatcher) SetClientFactory(codec ports.NotificationCredentialCodec, f ClientFactory) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.codec = codec
	d.factory = f
}
func validChannelConfig(kind string, c ChannelConfig) bool {
	switch kind {
	case "webhook":
		u, err := url.Parse(c.URL)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Fragment == "" && c.Secret != "" && c.BotToken == "" && c.ChatID == ""
	case "telegram":
		return strings.TrimSpace(c.BotToken) != "" && strings.TrimSpace(c.ChatID) != "" && c.URL == "" && c.Secret == ""
	}
	return false
}

// CreateChannel requires a current business administrator and returns no configuration.
func (d *Dispatcher) CreateChannel(ctx context.Context, actor *ports.Actor, name, kind string, c ChannelConfig, idempotency ...string) (*ports.NotificationChannelObservation, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Scopes != nil || !actor.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if strings.TrimSpace(name) == "" || !validChannelConfig(kind, c) {
		return nil, ErrInvalidConfig
	}
	d.mu.RLock()
	codec := d.codec
	d.mu.RUnlock()
	if codec == nil {
		return nil, ports.ErrUnavailable
	}
	var idemKey, requestHash string
	if len(idempotency) > 0 {
		if len(idempotency) != 1 || len(idempotency[0]) == 0 || len(idempotency[0]) > 160 {
			return nil, ErrInvalidConfig
		}
		idemKey = idempotency[0]
		canonical, _ := json.Marshal(map[string]any{"name": name, "type": kind, "config": c})
		digest, err := codec.DigestSecret(append([]byte("notification.channel.create.intent.v1\x00"), canonical...))
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		requestHash = "hmac.v1:" + digest
	}
	if d.channels == nil {
		return nil, ports.ErrUnavailable
	}
	plain, e := json.Marshal(c)
	if e != nil {
		return nil, ErrInvalidConfig
	}
	cipher, e := codec.EncryptSecret(plain)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	return d.channels.CreateNotificationChannel(ctx, actor, ports.NotificationChannelWrite{Name: name, ChannelType: kind, Ciphertext: cipher}, idemKey, requestHash)
}

// Legacy text is part of already emitted Webhook bodies and signatures.
// Localized media imports use the frozen notification.media_imported.v1 template.
func notificationText(kind string) string {
	switch kind {
	case "media.imported":
		return "Media imported into library"
	case "download.completed":
		return "Download completed"
	default:
		return "Media activity updated"
	}
}
