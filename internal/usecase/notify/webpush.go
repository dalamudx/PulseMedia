package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/webpush"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type WebPushService struct {
	repo   ports.WebPushRepository
	codec  ports.NotificationCredentialCodec
	config ports.WebPushPublicConfig
}

func NewWebPushService(repo ports.WebPushRepository, codec ports.NotificationCredentialCodec, publicKey string, enabled bool) *WebPushService {
	hash := sha256.Sum256([]byte(publicKey))
	config := ports.WebPushPublicConfig{Enabled: enabled && webpush.ValidPublicKey(publicKey), Version: hex.EncodeToString(hash[:]), PublicKey: publicKey}
	if !config.Enabled {
		config = ports.WebPushPublicConfig{}
	}
	return &WebPushService{repo: repo, codec: codec, config: config}
}

func webPushActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(a.ID)
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) || err != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	return nil
}
func webPushKey(key string) bool {
	return len(key) > 0 && len(key) <= 160 && strings.TrimSpace(key) == key
}

func (s *WebPushService) Configuration(ctx context.Context, a *ports.Actor) (*ports.WebPushPublicConfig, error) {
	if err := webPushActor(a); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	// Check current persisted authority even when the deployment switch is off.
	if _, err := s.repo.ListWebPush(ctx, a); err != nil {
		return nil, err
	}
	c := s.config
	return &c, nil
}

func (s *WebPushService) Register(ctx context.Context, a *ports.Actor, id uuid.UUID, version string, sub ports.WebPushSubscription, key string) (*ports.WebPushDevice, error) {
	if err := webPushActor(a); err != nil {
		return nil, err
	}
	if id == uuid.Nil || !webPushKey(key) || len(version) != 64 || !webpush.ValidSubscription(sub) {
		return nil, ErrInvalidConfig
	}
	if s == nil || s.repo == nil || s.codec == nil {
		return nil, ports.ErrUnavailable
	}
	raw, _ := json.Marshal([]any{"webpush.register.v1", id, version, sub})
	hash, err := s.codec.DigestSecret(raw)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.RegisterWebPush(ctx, a, id, key, "hmac.v1:"+hash, func() (ports.WebPushRegistration, error) {
		if !s.config.Enabled || version != s.config.Version {
			return ports.WebPushRegistration{}, ports.ErrWebPushConflict
		}
		endpointDigest, e := s.codec.DigestSecret(append([]byte("webpush.endpoint.v1\x00"), []byte(sub.Endpoint)...))
		if e != nil {
			return ports.WebPushRegistration{}, ports.ErrUnavailable
		}
		plain, _ := json.Marshal(sub)
		cipher, e := s.codec.EncryptSecret(plain)
		if e != nil {
			return ports.WebPushRegistration{}, ports.ErrUnavailable
		}
		return ports.WebPushRegistration{ID: id, VAPIDVersion: version, EndpointDigest: endpointDigest, Ciphertext: cipher}, nil
	})
}
func (s *WebPushService) Revoke(ctx context.Context, a *ports.Actor, id uuid.UUID, expected time.Time, key string) (*ports.WebPushDevice, error) {
	if err := webPushActor(a); err != nil {
		return nil, err
	}
	if id == uuid.Nil || expected.IsZero() || !webPushKey(key) {
		return nil, ErrInvalidConfig
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	raw, _ := json.Marshal([]any{"webpush.revoke.v1", id, expected.UTC()})
	hash := sha256.Sum256(raw)
	return s.repo.RevokeWebPush(ctx, a, id, expected.UTC(), key, hex.EncodeToString(hash[:]))
}
func (s *WebPushService) List(ctx context.Context, a *ports.Actor) ([]ports.WebPushDevice, error) {
	if err := webPushActor(a); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.ListWebPush(ctx, a)
}
func (s *WebPushService) Receipt(ctx context.Context, a *ports.Actor, key string) (*ports.WebPushDevice, error) {
	if err := webPushActor(a); err != nil {
		return nil, err
	}
	if !webPushKey(key) {
		return nil, ErrInvalidConfig
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.GetWebPushReceipt(ctx, a, key)
}
