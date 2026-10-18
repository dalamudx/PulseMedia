package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type IndexerMutationAction string

const (
	IndexerCreate IndexerMutationAction = "create"
	IndexerUpdate IndexerMutationAction = "update"
	IndexerDelete IndexerMutationAction = "delete"
)

// ErrIndexerCommandConflict covers stale versions and changed same-key requests.
var ErrIndexerCommandConflict = errors.New("indexer configuration or command version changed")

// IndexerManagementInput must never be logged. Nil update secret pointers preserve
// existing bytes; a nonnil empty credential explicitly clears the credential.
// Categories must be an explicit array, including an empty array to clear them.
type IndexerManagementInput struct {
	Name            string
	Protocol        IndexerProtocol
	URL             *string `json:"-"`
	Credential      *string `json:"-"`
	Categories      []int
	ExpectedVersion int64
}

type IndexerMutation struct {
	Action             IndexerMutationAction
	IndexerID          uuid.UUID
	Name               string
	ExpectedVersion    int64
	Configuration      PreparedIndexerConfiguration
	PreserveURL        bool
	PreserveCredential bool
}

// IndexerManagementView exposes only a safe URL origin and public health metadata.
type IndexerManagementView struct {
	ID                uuid.UUID
	Name              string
	URL               string
	Protocol          IndexerProtocol
	Categories        []int
	ConfigVersion     int64
	Health            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	LegacyV1          bool
	HealthCheckedAt   *time.Time
	HealthObservedAt  *time.Time
	HealthNextCheckAt *time.Time
	HealthFailures    int32
	HealthReason      string
}

type IndexerManagementReceipt struct {
	Action  IndexerMutationAction
	Indexer IndexerManagementView
	Deleted bool
}

// IndexerManagementRepository owns all blocking I/O and transaction work. Each
// mutation locks/revalidates the current account before reading receipts, locks
// an action-namespaced command key, checks versions, and commits configuration,
// audit, event and stable receipt atomically. Create receives uuid.Nil: the store
// allocates an ID only on the first invocation. Preserved secret fields contain
// no replacement bytes; the store retains their exact existing ciphertext.
// List must also revalidate the current account. Returned errors use safe ports
// sentinels; returned views contain no secret URL components or credentials.
type IndexerManagementRepository interface {
	MutateIndexer(ctx context.Context, actor *Actor, mutation IndexerMutation, key, hash string) (*IndexerManagementReceipt, error)
	ListManagedIndexers(ctx context.Context, actor *Actor) ([]IndexerManagementView, error)
}
