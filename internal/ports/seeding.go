package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
)

// SeedingRemoteTorrent is one exact original hash+operation-tag observation.
// It carries no removal authority; snapshots are revalidated by the execution transaction.
type SeedingRemoteTorrent struct {
	InfoHash     string
	OperationTag string
	SavePath     string
	State        string
	TotalBytes   int64
	Facts        seeding.Observation
}

// SeedingPathSpace is valid only for this exact native download path.
// Nil means unknown, including unsupported paths; a known zero remains zero.
type SeedingPathSpace struct {
	Path      string
	FreeBytes *int64
}

// SeedingReadClient exposes no Add, Remove or daemon configuration capability.
type SeedingReadClient interface {
	ReadSeedingTorrent(context.Context, string, string) (*SeedingRemoteTorrent, error)
	ReadSeedingFreeSpace(context.Context, string) (SeedingPathSpace, error)
}

// SeedingRemovalTarget is frozen by the permanent execution permit. A native
// capability must recheck these exact remote facts before a task-only removal.
type SeedingRemovalTarget struct {
	InfoHash     string
	OperationTag string
	SavePath     string
	TotalBytes   int64
}

// SeedingRemovalAuthorizer prepares or revalidates the permanent permit only
// after the last exact native identity read and before dispatch. A callback
// error prevents the POST; the callback must check the current DB authority,
// frozen policy/client/task graph, observation freshness and the original lease.
type SeedingRemovalAuthorizer func(context.Context, SeedingRemoteTorrent) error

type SeedingRemovalOutcome string

const (
	SeedingRemovalNotDispatched SeedingRemovalOutcome = "not_dispatched"
	SeedingRemovalAbsent        SeedingRemovalOutcome = "absent"
	SeedingRemovalAcknowledged  SeedingRemovalOutcome = "acknowledged"
	SeedingRemovalUnknown       SeedingRemovalOutcome = "unknown"
)

// SeedingRemovalClient exposes one attempt bound to one original native task.
// It cannot delete files, Add, or change daemon configuration. It does not grant
// business authority: the caller needs the persisted, lease-bound permit. An
// unknown result must only be observed, including after process restart.
type SeedingRemovalClient interface {
	RemoveRetainingFiles(context.Context) (SeedingRemovalOutcome, error)
}
