package ports

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

var ErrInteractiveSearchConflict = errors.New("interactive search command changed")

// Deferred means no external call was admitted because its persisted budget or
// physical endpoint permit is still occupied. It is never an empty success.
var ErrInteractiveSearchDeferred = errors.New("interactive search source deferred")

// InteractiveSearchInput creates an actor-owned observation, never acquisition authority.
type InteractiveSearchInput struct {
	Query          string
	MediaID        *uuid.UUID
	SubscriptionID *uuid.UUID
}

// PreparedInteractiveSearch freezes the usecase-selected display policy.
type PreparedInteractiveSearch struct {
	InteractiveSearchInput
	RankingPolicy quality.Policy
}

// Header is a stable command receipt. Query/scope/policy/deadline never change.
type InteractiveSearchHeader struct {
	ID             uuid.UUID
	ActorID        uuid.UUID
	Query          string
	MediaID        *uuid.UUID
	SubscriptionID *uuid.UUID
	CreatedAt      time.Time
	Deadline       time.Time
	CancelledAt    *time.Time
	SourceCount    int
	RankingPolicy  quality.Policy `json:"-"`
}

type InteractiveSearchSource struct {
	IndexerID     uuid.UUID
	Name          string
	ConfigVersion int64
	State         string
	Reason        string
	QueryIDs      []uuid.UUID
	Partial       bool
}

// Candidate and encrypted references stay internal; HTTP explicitly projects safe fields.
type InteractiveSearchCandidate struct {
	Candidate    SearchCandidate `json:"-"`
	SourceName   string
	QualityScore int
}

type InteractiveSearchSnapshot struct {
	Header          InteractiveSearchHeader
	State           string
	Sources         []InteractiveSearchSource
	Candidates      []InteractiveSearchCandidate
	Partial         bool
	Truncated       bool
	TotalCandidates int
	StatisticsMode  string
}

// Task is the complete persisted job payload. Lease is supplied by the claiming worker.
type InteractiveSearchSourceTask struct {
	SearchID      uuid.UUID `json:"search_id"`
	IndexerID     uuid.UUID `json:"indexer_id"`
	ConfigVersion int64     `json:"config_version"`
	Lease         JobLease  `json:"-"`
}

// SourceLease is reconstructed from durable scope after checking the job owner/token/state.
// Its values confer no authority until revalidated by the repository at each boundary.
type InteractiveSearchSourceLease struct {
	Task     InteractiveSearchSourceTask
	ActorID  uuid.UUID
	Query    string
	Deadline time.Time
}

// Each command revalidates the current account and object scope before reading receipts.
// Creation freezes all enabled nondeleted sources regardless of their health projection,
// inserts source jobs, audit, event and a stable receipt in the same transaction.
// The fixed observation window is 30 seconds and at most 64 sources are admitted;
// exceeding the cap rejects the command atomically, never silently truncates the set.
// Snapshots use a single repeatable-read transaction and do no writes or network I/O.
type InteractiveSearchRepository interface {
	CreateInteractiveSearch(context.Context, *Actor, PreparedInteractiveSearch, string, string) (*InteractiveSearchHeader, error)
	ReadInteractiveSearch(context.Context, *Actor, uuid.UUID) (*InteractiveSearchSnapshot, error)
	CancelInteractiveSearch(context.Context, *Actor, uuid.UUID, string, string) (*InteractiveSearchHeader, error)
	ReadInteractiveSearchReceipt(ctx context.Context, actor *Actor, key, action string) (*InteractiveSearchHeader, error)
	LoadInteractiveSearchSource(context.Context, InteractiveSearchSourceTask) (*InteractiveSearchSourceLease, error)
	FinishInteractiveSearchSource(context.Context, InteractiveSearchSourceLease, string) error
	ExpireInteractiveSearches(context.Context, int32) (int, error)
}

// The executor remains synchronous through actual physical I/O and performs no
// source-terminal write. The worker finishes through the original fenced lease.
// Completed pages are published atomically by the existing execution permit.
type InteractiveSearchExecutor interface {
	ExecuteInteractiveSearchSource(context.Context, InteractiveSearchSourceLease) error
}

// Interactive continuation uses lifecycle/configuration authority, not health.
type InteractiveSearchContinuationReader interface {
	InteractiveSearchContinuationDelay(context.Context, uuid.UUID, int64) (time.Duration, error)
}
