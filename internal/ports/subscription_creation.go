package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrNoSubscriptionRoot        = errors.New("no matching online subscription root")
	ErrAmbiguousSubscriptionRoot = errors.New("subscription root selection required")
)

// SubscriptionCreationInput is intent, not provider evidence. Nil bindings are
// preserved on an existing subscription; explicit empty bindings disable them.
// Nil ExpectedIntentVersion can ensure an equivalent existing intent, but cannot
// authorize a reconfiguration of that intent. MediaID and TMDBID are exclusive.
type SubscriptionCreationInput struct {
	UserID                      *uuid.UUID  `json:"user_id,omitempty"`
	MediaID                     *uuid.UUID  `json:"media_id,omitempty"`
	TMDBID                      *int64      `json:"tmdb_id,omitempty"`
	MediaType                   string      `json:"media_type,omitempty"`
	RootID                      *uuid.UUID  `json:"root_id,omitempty"`
	ClassificationVersion       *int64      `json:"classification_version,omitempty"`
	ClassificationPreviewStamp  string      `json:"classification_preview_stamp,omitempty"`
	ClassificationPreviewRootID *uuid.UUID  `json:"classification_preview_root_id,omitempty"`
	QualityPolicyID             string      `json:"quality_policy_id"`
	IndexerIDs                  []uuid.UUID `json:"indexer_ids"`
	DownloaderID                *uuid.UUID  `json:"downloader_id,omitempty"`
	ScopeType                   string      `json:"scope_type"`
	Seasons                     []int       `json:"seasons"`
	IncludeSpecials             bool        `json:"include_specials"`
	EpisodeKeys                 []string    `json:"episodes"`
	SelectedUnitIDs             []uuid.UUID `json:"selected_unit_ids"`
	ExpectedIntentVersion       *int64      `json:"expected_intent_version,omitempty"`
}

// A single typed value prevents swapping key and fingerprint at the boundary.
// The repository namespaces Key under subscription creation before persistence.
type SubscriptionCreationIdentity struct {
	Key, RequestHash string
}

// Context is a short, authorized database observation before provider I/O.
// Commit must compare it with locked current material, including any inferred
// root and existing media/intent, rather than treating it as authorization.
type SubscriptionCreationContext struct {
	OwnerID, MediaID, RootID           uuid.UUID
	MediaType                          string
	TMDBID                             int64
	MediaVersion                       int64
	RootGeneration, RootHealthRevision int64
	PolicyRevision                     int64
	Existing                           *SubscriptionRecord
}

type SubscriptionCreationWanted struct {
	ID          uuid.UUID `json:"id"`
	UnitID      uuid.UUID `json:"unit_id"`
	UnitKey     string    `json:"unit_key"`
	IsSkipped   bool      `json:"is_skipped"`
	IsSatisfied bool      `json:"is_satisfied"`
}

// The original result is committed with the intent and Wanted changes. Recovery
// authenticates the current account/object before returning this historical
// result; it neither rereads providers nor recomputes a latest-state response.
type SubscriptionCreationReceipt struct {
	Subscription SubscriptionRecord           `json:"subscription"`
	Wanted       []SubscriptionCreationWanted `json:"wanted"`
}

// Decide runs pure scope rules against locked material. Repositories retain an
// independent copy and revalidate the decision, preserving the existing intent
// transaction contract. It cannot perform provider I/O or publish on its own.
type SubscriptionCreationDecider func(SubscriptionCreateContext) (SubscriptionCreateDecision, error)

type SubscriptionCreationRepository interface {
	ReadSubscriptionCreationContext(context.Context, *Actor, SubscriptionCreationInput, SubscriptionCreationIdentity) (SubscriptionCreationContext, *SubscriptionCreationReceipt, error)
	CommitSubscriptionCreation(context.Context, *Actor, SubscriptionCreationInput, SubscriptionCreationContext, *PreparedSubscriptionCreationMetadata, SubscriptionCreationIdentity, SubscriptionCreationDecider) (*SubscriptionCreationReceipt, error)
	ReadSubscriptionCreationReceipt(context.Context, *Actor, string) (*SubscriptionCreationReceipt, error)
}
