package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
)

var ErrVersionConflict = ports.ErrSettingVersionConflict

type SettingItem = ports.SettingItem
type UpdateSettingRequest struct {
	Value           any   `json:"value"`
	ExpectedVersion int64 `json:"expected_version"`
}
type Service struct {
	repo   ports.SettingsRepository
	cipher ports.SettingsCipher
}

func NewService(repo ports.SettingsRepository, cipher ...ports.SettingsCipher) *Service {
	var c ports.SettingsCipher
	if len(cipher) > 0 {
		c = cipher[0]
	}
	return &Service{repo: repo, cipher: c}
}
func encryptedValue(value any) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	s, ok := m["ciphertext"].(string)
	return ok && s != ""
}
func publicSetting(row ports.SettingRecord) (*SettingItem, error) {
	var value any
	if e := json.Unmarshal(row.Value, &value); e != nil {
		return nil, ports.ErrUnavailable
	}
	if row.Key == "tmdb.api_key" {
		value = map[string]any{"configured": encryptedValue(value)}
	}
	return &SettingItem{Key: row.Key, Value: value, Version: row.Version, UpdatedAt: row.UpdatedAt}, nil
}
func (s *Service) GetSetting(ctx context.Context, key string) (*SettingItem, error) {
	row, e := s.repo.ReadSetting(ctx, key)
	if e != nil || row == nil {
		return nil, e
	}
	return publicSetting(*row)
}
func (s *Service) ListSettings(ctx context.Context, a *ports.Actor) ([]SettingItem, error) {
	if a == nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	rows, e := s.repo.ListSettings(ctx, a)
	if e != nil {
		return nil, e
	}
	out := make([]SettingItem, 0, len(rows))
	for _, row := range rows {
		v, e := publicSetting(row)
		if e != nil {
			return nil, e
		}
		out = append(out, *v)
	}
	return out, nil
}

// GetSecret is reserved for the metadata adapter; legacy plaintext is not served.
func (s *Service) GetSecret(ctx context.Context, key string) (string, error) {
	if key != "tmdb.api_key" {
		return "", errors.New("unknown secret setting")
	}
	row, e := s.repo.ReadSetting(ctx, key)
	if e != nil || row == nil {
		return "", e
	}
	var stored map[string]string
	if e = json.Unmarshal(row.Value, &stored); e != nil {
		return "", ports.ErrUnavailable
	}
	encoded := stored["ciphertext"]
	if encoded == "" {
		return "", errors.New("secret must be configured again")
	}
	data, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		return "", ports.ErrUnavailable
	}
	if s.cipher == nil {
		return "", ports.ErrUnavailable
	}
	plain, e := s.cipher.Decrypt(data)
	if e != nil {
		return "", ports.ErrUnavailable
	}
	return string(plain), nil
}

func (s *Service) ReadSettingReceipt(ctx context.Context, a *ports.Actor, key, requestKey string) (*ports.SettingCommandResult, error) {
	if e := command.AuthorizeSession(a, ports.RoleAdmin); e != nil {
		return nil, e
	}
	if key == "" || requestKey == "" || len(key) > 255 || len(requestKey) > 200 {
		return nil, ports.ErrInvalidSettingCommand
	}
	return s.repo.ReadSettingReceipt(ctx, a, key, requestKey)
}

func (s *Service) UpdateSetting(ctx context.Context, a *ports.Actor, key string, req UpdateSettingRequest, idempotencyKey string) (*ports.SettingCommandResult, error) {
	if e := command.AuthorizeSession(a, ports.RoleAdmin); e != nil {
		return nil, e
	}
	if strings.TrimSpace(key) == "" || len(key) > 255 || req.ExpectedVersion < 0 || len(idempotencyKey) > 200 {
		return nil, ports.ErrInvalidSettingCommand
	}
	fp, e := command.Fingerprint("setting.update", key, req)
	if e != nil {
		return nil, ports.ErrInvalidSettingCommand
	}
	legacy, e := command.LegacyPayloadFingerprint(req)
	if e != nil {
		return nil, ports.ErrInvalidSettingCommand
	}
	value := req.Value
	audit := any(req)
	if key == "tmdb.api_key" {
		m, ok := req.Value.(map[string]any)
		if !ok {
			return nil, ports.ErrInvalidSettingCommand
		}
		secret, ok := m["api_key"].(string)
		if !ok || strings.TrimSpace(secret) == "" {
			return nil, ports.ErrInvalidSettingCommand
		}
		if s.cipher == nil {
			return nil, ports.ErrUnavailable
		}
		sealed, e := s.cipher.Encrypt([]byte(secret))
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		value = map[string]any{"ciphertext": base64.StdEncoding.EncodeToString(sealed)}
		audit = map[string]any{"redacted": true}
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return nil, ports.ErrInvalidSettingCommand
	}
	auditRaw, e := json.Marshal(audit)
	if e != nil {
		return nil, ports.ErrInvalidSettingCommand
	}
	return s.repo.UpdateSetting(ctx, a, ports.SettingWrite{Key: key, ExpectedVersion: req.ExpectedVersion, Value: raw, Audit: auditRaw, Fingerprint: fp, LegacyFingerprint: legacy, IdempotencyKey: idempotencyKey})
}
