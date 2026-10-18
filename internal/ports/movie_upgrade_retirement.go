package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Retirement never follows ingestion automatically. The user confirms the
// current snapshot; a dedicated durable permit allows one physical attempt.
type MovieUpgradeRetirementPreview struct {
	OperationID      uuid.UUID `json:"operation_id"`
	OldFileID        uuid.UUID `json:"old_file_id"`
	NewFileID        uuid.UUID `json:"new_file_id"`
	OldRelativePath  string    `json:"old_relative_path"`
	NewRelativePath  string    `json:"new_relative_path"`
	InputStamp       string    `json:"input_stamp"`
	CurrentScore     int       `json:"current_score"`
	NewScore         int       `json:"new_score"`
	RetentionDays    int       `json:"retention_days"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

type ConfirmMovieUpgradeRetirement struct {
	OperationID uuid.UUID `json:"operation_id"`
	InputStamp  string    `json:"input_stamp"`
	Confirmed   bool      `json:"confirmed"`
}

type MovieUpgradeRetirementReceipt struct {
	OperationID      uuid.UUID `json:"operation_id"`
	CommandID        uuid.UUID `json:"command_id"`
	QueuedAt         time.Time `json:"queued_at"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

type MovieUpgradeRetirementWork struct {
	OperationID uuid.UUID
	ActorID     uuid.UUID
}

type MovieUpgradeRetirementRepository interface {
	ReadMovieUpgradeRetirementPreview(context.Context, *Actor, uuid.UUID, uuid.UUID) (*MovieUpgradeRetirementPreview, error)
	ReadMovieUpgradeRetirementReceipt(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*MovieUpgradeRetirementReceipt, error)
	ConfirmMovieUpgradeRetirement(context.Context, *Actor, uuid.UUID, string, ConfirmMovieUpgradeRetirement) (*MovieUpgradeRetirementReceipt, error)
}

type MovieUpgradeRetirementRuntime interface {
	PendingMovieUpgradeRetirements(context.Context, int) ([]MovieUpgradeRetirementWork, error)
	ExecuteMovieUpgradeRetirement(context.Context, *Actor, uuid.UUID) error
}
