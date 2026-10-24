package ports

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/google/uuid"
)

var ErrSeedingLeaseLost = errors.New("seeding execution lease expired or changed")

// Recovery is frozen from the permanent permit, not a worker retry count.
// Rule and protection are snapshots; Prepare must recheck the live graph.
type SeedingExecution struct {
	TaskID      uuid.UUID
	ClientID    uuid.UUID
	ClientStamp string
	Target      SeedingRemovalTarget
	Rule        seeding.Rule
	Protection  seeding.Protection
	ObserveOnly bool
}

type SeedingExecutionResult string

const (
	SeedingExecutionIneligible   SeedingExecutionResult = "ineligible"
	SeedingExecutionAbsent       SeedingExecutionResult = "absent"
	SeedingExecutionPresent      SeedingExecutionResult = "present"
	SeedingExecutionAcknowledged SeedingExecutionResult = "acknowledged"
	SeedingExecutionUnknown      SeedingExecutionResult = "unknown"
	SeedingExecutionFailed       SeedingExecutionResult = "failed"
)

// Acquire holds physical exclusion until Close. Reserve charges each native
// operation before dispatch; Check validates the original lease and live graph.
// Prepare commits a permanent, lease-bound permit and rechecks observed facts,
// policy, client, account and ingestion graph at commit. It cannot rebind a permit.
// Finish commits only evidence/audit/job state, never file or demand mutations.
type SeedingExecutionPermit interface {
	Snapshot() SeedingExecution
	Reserve(context.Context) error
	Check(context.Context) error
	Prepare(context.Context, SeedingRemoteTorrent) error
	Finish(context.Context, SeedingExecutionResult, error) error
	Close()
}
type SeedingExecutionRepository interface {
	Acquire(context.Context, JobLease) (SeedingExecutionPermit, error)
}

// Factories construct from the exact database fingerprint, without network or filesystem I/O.
// The remover's last native read is followed by the supplied authorizer and one
// non-replayed mutation. Recovery never constructs this capability.
type SeedingExecutionNative struct {
	Reader  SeedingReadClient
	Remover func(SeedingRemovalTarget, SeedingRemovalAuthorizer) (SeedingRemovalClient, error)
}
type SeedingExecutionFactory interface {
	OpenSeedingExecution(context.Context, uuid.UUID, string) (SeedingExecutionNative, error)
}
