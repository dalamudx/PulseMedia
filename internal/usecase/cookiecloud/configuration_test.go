package cookiecloud_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	cloud "github.com/dalamudx/PulseMedia/internal/usecase/cookiecloud"
	"github.com/google/uuid"
)

type configRepo struct {
	receipt      *ports.CookieCloudConfigReceipt
	digest       string
	reads, saves int
	prepared     ports.PreparedCookieCloudConfig
	err          error
}

func (f *configRepo) ReadCookieCloudConfig(context.Context, *ports.Actor) (*ports.CookieCloudConfigView, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	if f.receipt == nil {
		return nil, nil
	}
	v := f.receipt.Config
	return &v, nil
}
func (f *configRepo) ReadCookieCloudConfigReceipt(_ context.Context, _ *ports.Actor, key string, digest *string) (*ports.CookieCloudConfigReceipt, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	if f.receipt == nil {
		return nil, nil
	}
	if f.receipt.CommandKey != key {
		return nil, nil
	}
	if digest != nil && *digest != f.digest {
		return nil, ports.ErrCookieCloudConfigConflict
	}
	return f.receipt, nil
}
func (f *configRepo) SaveCookieCloudConfig(_ context.Context, a *ports.Actor, p ports.PreparedCookieCloudConfig, key, hash string) (*ports.CookieCloudConfigReceipt, error) {
	f.saves++
	f.prepared = p
	if f.err != nil {
		return nil, f.err
	}
	origin := p.ServerOrigin
	if p.PreserveConnection {
		origin = "https://cloud.example"
	}
	f.digest = hash
	f.receipt = &ports.CookieCloudConfigReceipt{CommandKey: key, Config: ports.CookieCloudConfigView{OwnerID: uuid.MustParse(a.ID), Version: p.ExpectedVersion + 1, Enabled: p.Enabled, IntervalSeconds: p.IntervalSeconds, ServerOrigin: origin, HasConnection: true, UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}}
	return f.receipt, nil
}

type configProtector struct {
	encrypted int
	err       error
}

func (p *configProtector) EncryptSecret([]byte) ([]byte, error) {
	p.encrypted++
	if p.err != nil {
		return nil, p.err
	}
	return []byte("protected-not-plaintext"), nil
}
func (p *configProtector) DigestSecret(data []byte) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func owner() *ports.Actor {
	return &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
}
func input() ports.CookieCloudConfigInput {
	return ports.CookieCloudConfigInput{Enabled: true, IntervalSeconds: 1200, Connection: &ports.CookieCloudConnection{ServerURL: "https://cloud.example/private/prefix", UserKey: "owned-user-key", Password: "owned-password"}}
}

func TestCookieCloudConfigEncryptAndOriginalKey(t *testing.T) {
	repo := &configRepo{}
	secret := &configProtector{}
	s := cloud.NewConfigService(repo, secret)
	a := owner()
	in := input()
	result, err := s.Save(t.Context(), a, in, "original")
	if err != nil || result.Config.ServerOrigin != "https://cloud.example" || repo.saves != 1 || secret.encrypted != 1 || strings.Contains(string(repo.prepared.ConnectionEncrypted), in.Connection.Password) {
		t.Fatal("configuration was not safely prepared", err)
	}
	restored, err := s.Save(t.Context(), a, in, "original")
	if err != nil || restored.Config != result.Config || repo.saves != 1 || secret.encrypted != 1 {
		t.Fatal("original key encrypted or saved again", err)
	}
	in.Enabled = false
	if _, err = s.Save(t.Context(), a, in, "original"); !errors.Is(err, ports.ErrCookieCloudConfigConflict) || repo.saves != 1 {
		t.Fatal("changed same-key input accepted")
	}
	if _, err = s.Receipt(t.Context(), a, "original"); err != nil || repo.saves != 1 {
		t.Fatal("receipt read modified configuration")
	}
}

func TestCookieCloudConfigHistoricalReceiptBeforeValidation(t *testing.T) {
	repo := &configRepo{}
	secret := &configProtector{}
	a := owner()
	in := input()
	in.IntervalSeconds = 59
	raw, _ := json.Marshal(in)
	repo.digest, _ = secret.DigestSecret(raw)
	repo.receipt = &ports.CookieCloudConfigReceipt{CommandKey: "old", Config: ports.CookieCloudConfigView{OwnerID: uuid.MustParse(a.ID), Version: 1, IntervalSeconds: 59, HasConnection: true, UpdatedAt: time.Now()}}
	if _, err := cloud.NewConfigService(repo, secret).Save(t.Context(), a, in, "old"); err != nil || secret.encrypted != 0 || repo.saves != 0 {
		t.Fatal("historical result blocked by new validation", err)
	}
}

func TestCookieCloudConfigPermissionAndCancellation(t *testing.T) {
	for _, kind := range []string{"nil", "member", "token", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			repo := &configRepo{}
			secret := &configProtector{}
			a := owner()
			ctx := t.Context()
			switch kind {
			case "nil":
				a = nil
			case "member":
				a.Role = ports.RoleMember
			case "token":
				a.Scopes = []string{}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := cloud.NewConfigService(repo, secret).Save(ctx, a, input(), "key"); err == nil || repo.reads != 0 || repo.saves != 0 || secret.encrypted != 0 {
				t.Fatal("unauthorized configuration reached persistence")
			}
		})
	}
}

func TestCookieCloudConfigOfflinePreservationAndFailure(t *testing.T) {
	repo := &configRepo{}
	secret := &configProtector{}
	s := cloud.NewConfigService(repo, secret)
	a := owner()
	in := ports.CookieCloudConfigInput{ExpectedVersion: 1, Enabled: false, IntervalSeconds: 1200}
	if _, err := s.Save(t.Context(), a, in, "offline-disable"); err != nil || !repo.prepared.PreserveConnection || len(repo.prepared.ConnectionEncrypted) != 0 || secret.encrypted != 0 {
		t.Fatal("offline deactivation replaced secrets", err)
	}
	repo.err = errors.New("private-storage-details")
	if _, err := s.Read(t.Context(), a); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("storage error not safely classified")
	}
	repo.err = nil
	repo.receipt.Config.OwnerID = uuid.New()
	if _, err := s.Receipt(t.Context(), a, "offline-disable"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("foreign-owner result accepted")
	}
}

func TestCookieCloudConfigValidationMakesNoWrites(t *testing.T) {
	for _, kind := range []string{"missing", "interval", "key", "password", "url", "version", "crypto"} {
		t.Run(kind, func(t *testing.T) {
			repo := &configRepo{}
			secret := &configProtector{}
			in := input()
			switch kind {
			case "missing":
				in.Connection = nil
			case "interval":
				in.IntervalSeconds = 0
			case "key":
				in.Connection.UserKey = "../private"
			case "password":
				in.Connection.Password = ""
			case "url":
				in.Connection.ServerURL += "?credential=private"
			case "version":
				in.ExpectedVersion = -1
			case "crypto":
				secret.err = errors.New("private-key-details")
			}
			if _, err := cloud.NewConfigService(repo, secret).Save(t.Context(), owner(), in, "invalid"); err == nil || repo.saves != 0 {
				t.Fatal("invalid configuration wrote state")
			}
		})
	}
}
