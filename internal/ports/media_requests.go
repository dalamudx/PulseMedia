package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/google/uuid"
	"time"
)

var ErrMediaRequestConflict = errors.New("media request changed")
var ErrMediaRequestLimit = errors.New("too many pending media requests")

type MediaRequest struct {
	ID             uuid.UUID           `json:"id"`
	OwnerID        uuid.UUID           `json:"owner_id"`
	Intent         mediarequest.Intent `json:"intent"`
	State          string              `json:"state"`
	Version        int64               `json:"version"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	DecidedBy      *uuid.UUID          `json:"decided_by,omitempty"`
	DecidedAt      *time.Time          `json:"decided_at,omitempty"`
	Reason         string              `json:"reason"`
	SubscriptionID *uuid.UUID          `json:"subscription_id,omitempty"`
}
type MediaRequestReceipt struct {
	CommandKey string       `json:"command_key"`
	Request    MediaRequest `json:"request"`
}
type MediaRequestCloseInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Action          string `json:"action"`
	Reason          string `json:"reason"`
}
type MediaRequestApprovalInput struct {
	ExpectedVersion int64       `json:"expected_version"`
	RootID          uuid.UUID   `json:"root_id"`
	QualityPolicyID string      `json:"quality_policy_id"`
	IndexerIDs      []uuid.UUID `json:"indexer_ids"`
	DownloaderID    *uuid.UUID  `json:"downloader_id,omitempty"`
}
type MediaRequestApprovalReceipt struct {
	RequestID    uuid.UUID                   `json:"request_id"`
	Subscription SubscriptionCreationReceipt `json:"subscription"`
}
type MediaRequestRepository interface {
	SubmitMediaRequest(context.Context, *Actor, mediarequest.Intent, string) (*MediaRequestReceipt, error)
	ReadMediaRequest(context.Context, *Actor, uuid.UUID) (*MediaRequest, error)
	ListMediaRequests(context.Context, *Actor, int, *uuid.UUID) ([]MediaRequest, error)
	ReadMediaRequestReceipt(context.Context, *Actor, string) (*MediaRequestReceipt, error)
	CloseMediaRequest(context.Context, *Actor, uuid.UUID, MediaRequestCloseInput, string) (*MediaRequestReceipt, error)
}

// Persistent rows, rather than this factory input, carry identity and scope authority.
type MediaRequestApprovalFence struct {
	RequestID       uuid.UUID
	ExpectedVersion int64
	Key             string
}
type SubscriptionCreator interface {
	Create(context.Context, *Actor, SubscriptionCreationInput, string) (*SubscriptionCreationReceipt, error)
	Receipt(context.Context, *Actor, string) (*SubscriptionCreationReceipt, error)
}

// App constructs each scoped creator with the same provider and publication UoW.
type MediaRequestApprovalCreatorFactory func(MediaRequestApprovalFence) SubscriptionCreator
