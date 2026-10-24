package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const UntrackedDiscoveryJobKind = "untracked_discovery"

var ErrUntrackedDiscoveryStale = errors.New("untracked discovery authority is no longer current")

type UntrackedDiscoverySourcePage struct {
	Scopes     []UntrackedScope
	NextCursor string
	Complete   bool
	ManifestAt time.Time
}

type UntrackedDiscoveryScheduleResult struct {
	JobID  uuid.UUID
	Queued bool
}

type UntrackedDiscoveryScheduler interface {
	ListUntrackedDiscoveryScopes(context.Context, string, int) (UntrackedDiscoverySourcePage, error)
	RequestUntrackedDiscovery(context.Context, uuid.UUID, int64) (*UntrackedDiscoveryScheduleResult, error)
	QueueDueUntrackedDiscoveries(context.Context, int) (int, error)
}

type UntrackedDiscoverySnapshot struct {
	Lease    JobLease
	Scope    UntrackedScope
	Sequence int64
}

// A repository-issued capture binds observations to the original lease and scope.
type UntrackedDiscoveryCapture interface {
	UntrackedDiscoverySnapshot() UntrackedDiscoverySnapshot
}

type UntrackedFileObservation struct {
	RelativePath string
	Identity     FileIdentity
}

type UntrackedDiscoveryRepository interface {
	LoadUntrackedDiscovery(context.Context, JobLease) (UntrackedDiscoveryCapture, error)
	ValidateUntrackedDiscovery(context.Context, UntrackedDiscoveryCapture) error
	RecordUntrackedDiscovery(context.Context, UntrackedDiscoveryCapture, []UntrackedFileObservation) error
	CompleteUntrackedDiscovery(context.Context, UntrackedDiscoveryCapture) error
}

type UntrackedDiscoveredFile struct {
	ID              uuid.UUID
	ScopeID         uuid.UUID
	ScopeVersion    int64
	Revision        int64
	RelativePath    string
	Identity        FileIdentity
	FirstObservedAt time.Time
	LastObservedAt  time.Time
	Observations    int64
}

type UntrackedDiscoveryPage struct {
	Files      []UntrackedDiscoveredFile
	NextCursor string
	Complete   bool
}

type UntrackedDiscoveryReader interface {
	ReadUntrackedDiscovery(context.Context, *Actor, uuid.UUID, string, int) (UntrackedDiscoveryPage, error)
}
