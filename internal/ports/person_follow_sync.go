package ports

import (
	"context"

	"github.com/google/uuid"
)

const PersonFollowSyncJobKind = "person_follow_sync"
const PersonFollowPageSize = 64

// A job consumes one page of a sealed observation; later pages do not reread credits.
type PersonFollowSyncWork struct {
	RunID        uuid.UUID
	ExecutionID  uuid.UUID
	Config       PersonFollowConfig
	Actor        Actor
	Start        int
	SourceStored bool
}
type PersonFollowSyncItem struct {
	ID          uuid.UUID
	Observation PersonWork
	RootID      uuid.UUID
	State       string
	CommandKey  string
}
type PersonFollowCreationFence struct {
	Lease         JobLease
	RunID         uuid.UUID
	ExecutionID   uuid.UUID
	ConfigID      uuid.UUID
	ConfigVersion int64
	ItemID        uuid.UUID
}

// The concrete creator adds the fence to the existing subscription creation transaction.
type PersonFollowSubscriptionCreator interface {
	CreatePersonFollowSubscription(context.Context, PersonFollowCreationFence, *Actor, SubscriptionCreationInput, string) (*SubscriptionCreationReceipt, error)
}

// Close follows the return of every physical call. Lost sessions without release
// evidence cannot authorize another execution or accept late provider results.
type PersonFollowSyncPermit interface {
	Snapshot() PersonFollowSyncWork
	Check(context.Context) error
	StoreCredits(context.Context, PersonCredits) error
	Items(context.Context) ([]PersonFollowSyncItem, error)
	ReviewItem(context.Context, uuid.UUID) error
	FinishPage(context.Context) error
	Close()
}
type PersonFollowSyncRepository interface {
	EnqueueDue(context.Context) error
	RecoverExpired(context.Context) error
	Acquire(context.Context, JobLease) (PersonFollowSyncPermit, error)
}
