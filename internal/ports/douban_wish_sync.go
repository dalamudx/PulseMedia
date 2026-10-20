package ports

import (
	"context"
	"github.com/google/uuid"
)

const DoubanWishSyncJobKind = "douban_wish_sync"

// Each job observes one bounded page. Continuations and the final item ledger
// are durable; no whole-list snapshot or deletion authority is implied.
type DoubanWishSyncWork struct {
	RunID       uuid.UUID
	Config      DoubanSyncConfig
	Actor       Actor
	MediaType   string
	Start       int
	ExecutionID uuid.UUID
}

type DoubanWishSyncItem struct {
	ID          uuid.UUID
	Observation DoubanWishItem
	State       string
	CommandKey  string
}

type DoubanWishMatch struct {
	State     string // matched, unresolved, incomplete, ambiguous
	TMDBID    int64
	MediaType string
}

type DoubanWishCreationFence struct {
	Lease                                JobLease
	RunID, ConfigID, ItemID, ExecutionID uuid.UUID
	ConfigVersion                        int64
}

// This factory boundary constructs the existing subscription creation service
// with a concrete repository that shares its transaction and adds these fences.
type DoubanWishSubscriptionCreator interface {
	CreateDoubanWishSubscription(context.Context, DoubanWishCreationFence, *Actor, SubscriptionCreationInput, string) (*SubscriptionCreationReceipt, error)
}

// The dedicated physical permit is held until all provider calls return. Loss
// of its database session prevents writes and forbids automatic replay.
type DoubanWishSyncPermit interface {
	Snapshot() DoubanWishSyncWork
	Check(context.Context) error
	StorePage(context.Context, DoubanWishPage) ([]DoubanWishSyncItem, error)
	StoreIdentity(context.Context, uuid.UUID, DoubanWishMatch) error
	FinishItem(context.Context, uuid.UUID, string) error
	FinishPage(context.Context) error
	Close()
}

type DoubanWishSyncRepository interface {
	EnqueueDue(context.Context) error
	RecoverExpired(context.Context) error
	Acquire(context.Context, JobLease) (DoubanWishSyncPermit, error)
}
