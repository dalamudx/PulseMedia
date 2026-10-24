package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/google/uuid"
	"time"
)

var ErrSeedingConfigConflict = errors.New("seeding configuration version or command conflict")

type SeedingConfigInput struct {
	ExpectedVersion         int64
	ExpectedClientUpdatedAt time.Time
	Enabled                 bool
	IntervalSeconds         int32
	Rule                    seeding.Rule
}
type SeedingConfigView struct {
	ClientID        uuid.UUID
	OwnerID         uuid.UUID
	Version         int64
	ClientUpdatedAt time.Time
	Enabled         bool
	IntervalSeconds int32
	Rule            seeding.Rule
	UpdatedAt       time.Time
}
type SeedingConfigReceipt struct {
	CommandKey string
	Config     SeedingConfigView
}

// The actor is always a current administrator session. Receipt absence is nil,nil;
// a nonnil fingerprint must match the original command before newer validation.
// Save locks the account, original key, client and configuration, validates both
// versions, and commits config/audit/event/receipt together without scheduling I/O.
type SeedingConfigRepository interface {
	ReadSeedingConfig(context.Context, *Actor, uuid.UUID) (*SeedingConfigView, error)
	ReadSeedingConfigReceipt(context.Context, *Actor, uuid.UUID, string, *string) (*SeedingConfigReceipt, error)
	SaveSeedingConfig(context.Context, *Actor, uuid.UUID, SeedingConfigInput, string, string) (*SeedingConfigReceipt, error)
}
