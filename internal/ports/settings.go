package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrSettingVersionConflict = errors.New("setting version conflict")
var ErrCommandConflict = errors.New("command request conflict")
var ErrInvalidSettingCommand = errors.New("invalid setting command")

type SettingItem struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}
type SettingRecord struct {
	Key       string
	Value     json.RawMessage
	Version   int64
	UpdatedAt time.Time
}
type SettingWrite struct {
	Key               string
	ExpectedVersion   int64
	Value             json.RawMessage
	Audit             json.RawMessage
	Fingerprint       string
	LegacyFingerprint string
	IdempotencyKey    string
}

type SettingsCipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}

type SettingCommandResult struct {
	Data       *SettingItem
	StatusCode int
	ResourceID string
	FromCache  bool
}
type SettingsRepository interface {
	ReadSettingReceipt(context.Context, *Actor, string, string) (*SettingCommandResult, error)
	ReadSetting(context.Context, string) (*SettingRecord, error)
	ListSettings(context.Context, *Actor) ([]SettingRecord, error)
	UpdateSetting(context.Context, *Actor, SettingWrite) (*SettingCommandResult, error)
}
