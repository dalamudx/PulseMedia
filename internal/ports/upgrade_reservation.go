package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

var ErrUpgradeReservationConflict = errors.New("upgrade reservation inputs changed or unsupported")
var ErrUpgradeNotExecutable = errors.New("frozen upgrade reservation is not executable")

type UpgradeMode = quality.UpgradeMode

const (
	UpgradeDisabled        = quality.UpgradeDisabled
	UpgradeStopAtCutoff    = quality.UpgradeStopAtCutoff
	UpgradeReplaceIfBetter = quality.UpgradeReplaceIfBetter
)

type SetUpgradeModeCommand struct {
	SubscriptionID uuid.UUID   `json:"subscription_id"`
	ExpectedIntent int64       `json:"expected_intent"`
	Mode           UpgradeMode `json:"mode"`
	Confirmed      bool        `json:"confirmed"`
}
type UpgradeModeResult struct {
	SubscriptionID uuid.UUID   `json:"subscription_id"`
	IntentVersion  int64       `json:"intent_version"`
	Mode           UpgradeMode `json:"mode"`
}

// Expected is an exact observation, never an authorization. No path, score,
// profile, downloader configuration or policy rule is supplied by the caller.
type ReserveUpgradeCommand struct {
	SubscriptionID       uuid.UUID             `json:"subscription_id"`
	CurrentFileID        uuid.UUID             `json:"current_file_id"`
	CandidateID          uuid.UUID             `json:"candidate_id"`
	Expected             UpgradePreviewVersion `json:"expected"`
	ExpectedClientDigest string                `json:"expected_client_digest"`
	Confirmed            bool                  `json:"confirmed"`
}
type UpgradeReservationResult struct {
	OperationID      uuid.UUID `json:"operation_id"`
	ReservationID    uuid.UUID `json:"reservation_id"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type UpgradeReservationRepository interface {
	SetUpgradeMode(context.Context, *Actor, SetUpgradeModeCommand, string) (*UpgradeModeResult, error)
	ReserveUpgrade(context.Context, *Actor, ReserveUpgradeCommand, string) (*UpgradeReservationResult, error)
}
