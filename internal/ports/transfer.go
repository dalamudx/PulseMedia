package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

var ErrTransferReviewRequired = errors.New("transfer interrupted or target conflict: manual review required")
var ErrTransferExecutionRejected = errors.New("transfer execution owner, token, state or lease is invalid")

type MovieTransferRequest struct {
	OperationID, RootID, UnitID uuid.UUID
	OldFileID                   *uuid.UUID
	SourcePath, Title           string
	Year                        int
	Quality                     quality.Profile
}
type TransferPlan struct {
	ID, OperationID, RootID uuid.UUID
	State, Strategy         string
}
type TransferStep struct {
	ID, PlanID, UnitID             uuid.UUID
	SourcePath, TargetPath, Action string
	StepOrder                      int32
}
type PreparedMovieTransfer struct {
	Plan TransferPlan
	Step TransferStep
}

// The implementation owns short transactions and filesystem capabilities. No transaction leaks through this boundary.
type TransferRepository interface {
	PrepareMovieTransfer(context.Context, MovieTransferRequest) (PreparedMovieTransfer, error)
	ExecuteTransfer(context.Context, uuid.UUID) error
	OrganizeCompletedDownload(context.Context, JobLease) error
}
