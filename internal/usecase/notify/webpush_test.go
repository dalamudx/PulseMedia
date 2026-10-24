package notify_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

type pushRepo struct {
	calls    int
	replay   bool
	hash     string
	input    ports.WebPushRegistration
	expected time.Time
}

func (r *pushRepo) RegisterWebPush(_ context.Context, _ *ports.Actor, id uuid.UUID, _ string, hash string, p ports.WebPushRegistrationPrepare) (*ports.WebPushDevice, error) {
	r.calls++
	r.hash = hash
	if !r.replay {
		var err error
		r.input, err = p()
		if err != nil {
			return nil, err
		}
	}
	return &ports.WebPushDevice{ID: id, Status: "active"}, nil
}
func (r *pushRepo) RevokeWebPush(_ context.Context, _ *ports.Actor, id uuid.UUID, expected time.Time, _ string, hash string) (*ports.WebPushDevice, error) {
	r.calls++
	r.expected = expected
	r.hash = hash
	return &ports.WebPushDevice{ID: id, Status: "revoked"}, nil
}
func (r *pushRepo) ListWebPush(context.Context, *ports.Actor) ([]ports.WebPushDevice, error) {
	r.calls++
	return []ports.WebPushDevice{}, nil
}
func (r *pushRepo) GetWebPushReceipt(context.Context, *ports.Actor, string) (*ports.WebPushDevice, error) {
	r.calls++
	return &ports.WebPushDevice{ID: uuid.New(), Status: "active"}, nil
}

type pushCodec struct {
	encrypt int
	fail    bool
}

func (c *pushCodec) EncryptSecret(raw []byte) ([]byte, error) {
	c.encrypt++
	if c.fail {
		return nil, errors.New("secret unavailable")
	}
	return append(make([]byte, 40), raw...), nil
}
func (c *pushCodec) DigestSecret(raw []byte) (string, error) {
	return fmt.Sprintf("%064x", len(raw)), nil
}
func (c *pushCodec) DecryptSecret([]byte) ([]byte, error) { return nil, errors.New("not used") }
func pushInput(t *testing.T) (*ports.Actor, ports.WebPushSubscription, string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	return &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, ports.WebPushSubscription{Endpoint: "https://fcm.googleapis.com/browser-owned", P256DH: public, Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}, public
}
func TestWebPushDeviceAuthorityBeforeStorage(t *testing.T) {
	actor, sub, public := pushInput(t)
	r := &pushRepo{}
	s := notify.NewWebPushService(r, &pushCodec{}, public, true)
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleMember, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleAdmin}} {
		if _, err := s.Register(t.Context(), a, uuid.New(), strings.Repeat("a", 64), sub, "key"); !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) {
			t.Fatal(err)
		}
		if _, err := s.List(t.Context(), a); err == nil {
			t.Fatal("list bypass")
		}
		if _, err := s.Receipt(t.Context(), a, "key"); err == nil {
			t.Fatal("receipt bypass")
		}
	}
	if r.calls != 0 {
		t.Fatal("invalid actor entered storage")
	}
}
func TestWebPushDeviceValidationBeforeEncryption(t *testing.T) {
	a, sub, public := pushInput(t)
	r := &pushRepo{}
	codec := &pushCodec{}
	s := notify.NewWebPushService(r, codec, public, true)
	for _, mutate := range []func(*ports.WebPushSubscription){func(s *ports.WebPushSubscription) { s.Endpoint = "http://fcm.googleapis.com/a" }, func(s *ports.WebPushSubscription) { s.Endpoint = "https://127.0.0.1/a" }, func(s *ports.WebPushSubscription) { s.P256DH = "bad" }, func(s *ports.WebPushSubscription) { s.Auth = "bad" }} {
		copy := sub
		mutate(&copy)
		if _, err := s.Register(t.Context(), a, uuid.New(), strings.Repeat("a", 64), copy, "key"); !errors.Is(err, notify.ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
	if r.calls != 0 || codec.encrypt != 0 {
		t.Fatal("invalid input entered storage")
	}
}
func TestWebPushDeviceRegistrationAndFrozenConfig(t *testing.T) {
	a, sub, public := pushInput(t)
	r := &pushRepo{}
	codec := &pushCodec{}
	s := notify.NewWebPushService(r, codec, public, true)
	config, err := s.Configuration(t.Context(), a)
	if err != nil || !config.Enabled || config.PublicKey != public || len(config.Version) != 64 {
		t.Fatal(config, err)
	}
	id := uuid.New()
	result, err := s.Register(t.Context(), a, id, config.Version, sub, "register")
	if err != nil || result.ID != id || codec.encrypt != 1 || r.input.ID != id || r.input.VAPIDVersion != config.Version || len(r.input.EndpointDigest) != 64 {
		t.Fatal(result, err)
	}
	if _, err = s.Register(t.Context(), a, id, strings.Repeat("a", 64), sub, "changed"); !errors.Is(err, ports.ErrWebPushConflict) || codec.encrypt != 1 {
		t.Fatal("stale VAPID encrypted", err)
	}
	codec.fail = true
	r.replay = true
	disabled := notify.NewWebPushService(r, codec, "", false)
	if _, err = disabled.Register(t.Context(), a, id, config.Version, sub, "register"); err != nil || codec.encrypt != 1 {
		t.Fatal("receipt used changed deployment or encryption", err)
	}
}
func TestWebPushDeviceRevokeCanonicalAndOfflineReceipt(t *testing.T) {
	a, _, _ := pushInput(t)
	r := &pushRepo{}
	s := notify.NewWebPushService(r, nil, "", false)
	id := uuid.New()
	now := time.Date(2026, 10, 23, 0, 0, 0, 123000, time.UTC)
	if _, err := s.Revoke(t.Context(), a, id, now, "key"); err != nil {
		t.Fatal(err)
	}
	hash := r.hash
	if _, err := s.Revoke(t.Context(), a, id, now.In(time.FixedZone("east", 8*3600)), "key"); err != nil || r.hash != hash || r.expected.Location() != time.UTC {
		t.Fatal("timezone altered command", err)
	}
	if _, err := s.Receipt(t.Context(), a, "key"); err != nil {
		t.Fatal("offline receipt requires codec", err)
	}
	if _, err := s.Revoke(t.Context(), a, id, time.Time{}, "key"); !errors.Is(err, notify.ErrInvalidConfig) {
		t.Fatal(err)
	}
}
func TestWebPushDeviceUnavailableAndDisabled(t *testing.T) {
	a, _, _ := pushInput(t)
	s := notify.NewWebPushService(&pushRepo{}, nil, "bad", true)
	c, err := s.Configuration(t.Context(), a)
	if err != nil || c.Enabled || c.PublicKey != "" || c.Version != "" {
		t.Fatal("invalid deployment exposed", err)
	}
	var missing *notify.WebPushService
	if _, err := missing.List(t.Context(), a); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
}
