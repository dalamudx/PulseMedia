package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrSubscriptionBindingsConflict = errors.New("subscription intent or command changed")
var ErrInvalidSubscriptionBindings = errors.New("invalid subscription bindings")

type SubscriptionBindingsCommand struct {
	IndexerIDs      []uuid.UUID `json:"indexer_ids"`
	DownloaderID    *uuid.UUID  `json:"downloader_id"`
	ExpectedVersion int64       `json:"expected_version"`
	QualityPolicyID *string     `json:"quality_policy_id,omitempty"`
}
type SubscriptionBindingsInfo struct {
	ID            uuid.UUID   `json:"id"`
	State         string      `json:"state"`
	IntentVersion int64       `json:"intent_version"`
	IndexerIDs    []uuid.UUID `json:"indexer_ids"`
	DownloaderID  *uuid.UUID  `json:"downloader_id"`
}

// Mutations commit bindings, wanted intent, event, success audit and command result together.
type SubscriptionBindingRepository interface {
	ConfigureBindings(context.Context, *Actor, uuid.UUID, SubscriptionBindingsCommand, string, string) (*SubscriptionBindingsInfo, error)
}
