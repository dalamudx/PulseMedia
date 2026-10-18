package ports

import (
	"context"
	"github.com/google/uuid"
)

// ScanSeal binds a complete traversal to the physical directory and DB snapshot.
type ScanSeal struct {
	RunID, RootID                                           uuid.UUID
	Path, MediaType, MarkerFilename                         string
	Generation, HealthRevision, ScanToken, StartMutationSeq int64
	RootIdentity                                            FileIdentity
}

type ScanFinalizationProgress struct {
	RunID                             uuid.UUID
	Observed, Added, Updated, Missing int64
	MissingReviewRequired             bool
	Done                              bool
}

// ScanRepository persists positive observations, seals a verified complete walk,
// and advances a bounded reconciliation cursor. Filesystem I/O stays outside.
type ScanRepository interface {
	ScanObservationWriter
	LoadRoot(context.Context, uuid.UUID) (ScanRootSnapshot, error)
	UpdateProbedHealth(context.Context, ScanRootSnapshot, string) (ScanRootSnapshot, error)
	Start(context.Context, ScanRootSnapshot, uuid.UUID) (ScanRunSnapshot, error)
	Interrupt(context.Context, ScanSeal) error
	FindFinalizing(context.Context, uuid.UUID) (*ScanSeal, error)
	Seal(context.Context, ScanSeal) error
	ReconcileBatch(context.Context, ScanSeal) (ScanFinalizationProgress, error)
	FailFinalization(context.Context, ScanSeal) error
	AbortFinalization(context.Context, ScanSeal) error
}
