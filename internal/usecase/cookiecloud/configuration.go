package cookiecloud

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ConfigService struct {
	repo      ports.CookieCloudConfigRepository
	protector ports.SecretProtector
}

func NewConfigService(repo ports.CookieCloudConfigRepository, protector ports.SecretProtector) *ConfigService {
	return &ConfigService{repo: repo, protector: protector}
}

func configActor(ctx context.Context, a *ports.Actor) (ports.Actor, error) {
	if err := ctx.Err(); err != nil {
		return ports.Actor{}, err
	}
	if a == nil {
		return ports.Actor{}, ports.ErrUnauthorized
	}
	id, err := uuid.Parse(a.ID)
	if err != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.Actor{}, ports.ErrForbidden
	}
	return *a, nil
}
func configKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == ':', r == '.':
		default:
			return false
		}
	}
	return true
}
func configError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrCookieCloudConfigConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func validConfig(c *ports.CookieCloudConfigView, a ports.Actor) bool {
	return c != nil && c.OwnerID.String() == a.ID && c.Version > 0 && c.IntervalSeconds > 0 && c.HasConnection && !c.UpdatedAt.IsZero()
}
func validReceipt(r *ports.CookieCloudConfigReceipt, a ports.Actor, key string) bool {
	return r != nil && r.CommandKey == key && validConfig(&r.Config, a)
}

func (s *ConfigService) Read(ctx context.Context, actor *ports.Actor) (*ports.CookieCloudConfigView, error) {
	a, err := configActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	row, err := s.repo.ReadCookieCloudConfig(ctx, &a)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if row == nil {
		return nil, nil
	}
	if !validConfig(row, a) {
		return nil, ports.ErrUnavailable
	}
	out := *row
	return &out, nil
}
func (s *ConfigService) Receipt(ctx context.Context, actor *ports.Actor, key string) (*ports.CookieCloudConfigReceipt, error) {
	a, err := configActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !configKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	row, err := s.repo.ReadCookieCloudConfigReceipt(ctx, &a, key, nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if row == nil {
		return nil, ports.ErrNotFound
	}
	if !validReceipt(row, a, key) {
		return nil, ports.ErrUnavailable
	}
	out := *row
	return &out, nil
}
func (s *ConfigService) Save(ctx context.Context, actor *ports.Actor, in ports.CookieCloudConfigInput, key string) (*ports.CookieCloudConfigReceipt, error) {
	a, err := configActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !configKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.protector == nil {
		return nil, ports.ErrUnavailable
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 32768 {
		return nil, ports.ErrRejected
	}
	defer clear(raw)
	digest, err := s.protector.DigestSecret(raw)
	if err != nil || digest == "" {
		return nil, ports.ErrUnavailable
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	original, err := s.repo.ReadCookieCloudConfigReceipt(ctx, &a, key, &digest)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if original != nil {
		if !validReceipt(original, a, key) {
			return nil, ports.ErrUnavailable
		}
		out := *original
		return &out, nil
	}
	// Historical same-key results restore before applying today's validation.
	if in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || in.IntervalSeconds < 60 || in.IntervalSeconds > 86400 || (in.ExpectedVersion == 0 && in.Connection == nil) {
		return nil, ports.ErrRejected
	}
	prepared := ports.PreparedCookieCloudConfig{ExpectedVersion: in.ExpectedVersion, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, PreserveConnection: in.Connection == nil}
	if in.Connection != nil {
		c := *in.Connection
		endpoint, e := url.Parse(c.ServerURL)
		if e != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.RawPath != "" || len(c.ServerURL) > 2048 || c.UserKey == "" || len(c.UserKey) > 128 || c.Password == "" || len(c.Password) > 4096 || !utf8.ValidString(c.Password) {
			return nil, ports.ErrRejected
		}
		for _, r := range c.UserKey {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			default:
				return nil, ports.ErrRejected
			}
		}
		encoded, e := json.Marshal(c)
		if e != nil {
			return nil, ports.ErrRejected
		}
		defer clear(encoded)
		prepared.ConnectionEncrypted, e = s.protector.EncryptSecret(encoded)
		if e != nil || len(prepared.ConnectionEncrypted) == 0 {
			return nil, ports.ErrUnavailable
		}
		prepared.ServerOrigin = endpoint.Scheme + "://" + strings.ToLower(endpoint.Host)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	row, err := s.repo.SaveCookieCloudConfig(ctx, &a, prepared, key, digest)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if !validReceipt(row, a, key) || row.Config.Version != in.ExpectedVersion+1 || row.Config.Enabled != in.Enabled || row.Config.IntervalSeconds != in.IntervalSeconds || (!prepared.PreserveConnection && row.Config.ServerOrigin != prepared.ServerOrigin) {
		return nil, ports.ErrUnavailable
	}
	out := *row
	return &out, nil
}
