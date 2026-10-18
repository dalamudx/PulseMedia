package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// SeasonEpisodes is trusted metadata fetched before entering an intent transaction.
type SeasonEpisodes struct{ Season, Count int }
type SubscriptionMediaInput struct {
	Namespace, ExternalID, Title, MediaType string
	Year                                    *int32
	Seasons                                 []SeasonEpisodes
}
type CreateSubscriptionCommand struct {
	MediaInput            *SubscriptionMediaInput `json:"-"`
	EpisodeKeys           []string                `json:"episodes,omitempty"`
	UserID                *uuid.UUID              `json:"user_id,omitempty"`
	MediaID               uuid.UUID               `json:"media_id"`
	RootID                uuid.UUID               `json:"root_id"`
	QualityPolicyID       string                  `json:"quality_policy_id"`
	QualityPolicyRevision int64                   `json:"quality_policy_revision"`
	IndexerIDs            []uuid.UUID             `json:"indexer_ids"`
	DownloaderID          *uuid.UUID              `json:"downloader_id,omitempty"`
	ScopeType             string                  `json:"scope_type"`
	SelectedUnitIDs       []uuid.UUID             `json:"selected_unit_ids,omitempty"`
	IncludeSpecials       bool                    `json:"include_specials,omitempty"`
	Seasons               []int                   `json:"seasons,omitempty"`
}

type SubscriptionRecord struct {
	ID, UserID, MediaID, RootID uuid.UUID
	QualityPolicyID             string
	QualityPolicyRevision       int64
	IndexerIDs                  []uuid.UUID
	DownloaderID                *uuid.UUID
	State                       string
	IntentVersion               int64
	ScopeType                   string
	CreatedAt, UpdatedAt        time.Time
	IncludeSpecials             bool
	Seasons                     []int
	SelectedUnitIDs             []uuid.UUID
	EpisodeMappingRevision      *int64
	UpgradeMode                 string
}
type SubscriptionWanted struct {
	ID, SubscriptionID, UnitID                                                         uuid.UUID
	IntentVersion                                                                      int64
	IsSkipped, IsSatisfied, UserSkipped, ScopeExcluded                                 bool
	CurrentFactStage                                                                   *string
	LastEvaluatedAt, NextEvaluateAt, RssSeenAt                                         *time.Time
	CreatedAt, UpdatedAt                                                               time.Time
	EvaluatedIntentVersion, EvaluatedPolicyRevision                                    *int64
	EvaluatedPolicyID                                                                  *string
	EvaluatedRootID, EvaluatedUnitID, EvaluatedMediaID                                 *uuid.UUID
	EvaluatedRootGeneration, EvaluatedHealthRevision                                   *int64
	EvaluatedMediaMetadataVersion, EvaluatedUnitMetadataVersion, EvaluatedFactsVersion *int64
	EvaluatedState                                                                     *string
	EvaluatedSkipped                                                                   *bool
	ProjectionValidUntil                                                               *time.Time
	EvaluatedContractVersion                                                           *string
	EvaluatedWriteToken                                                                *uuid.UUID
}

// Unit specifications are chosen by the use case. Stores only persist them.
type SubscriptionMediaUnitSpec struct {
	UnitKey                     string
	SeasonNumber, EpisodeNumber *int32
}
type SubscriptionIntentUnit struct {
	ID                          uuid.UUID
	UnitKey                     string
	SeasonNumber, EpisodeNumber *int32
	Retired                     bool
}
type SubscriptionCreateRequest struct {
	Actor      Actor
	OwnerID    uuid.UUID
	Command    CreateSubscriptionCommand
	MediaUnits []SubscriptionMediaUnitSpec
}
type SubscriptionCreateContext struct {
	MediaID                              uuid.UUID
	MediaType, RootMediaType, RootHealth string
	PolicyRevision                       int64
	Existing                             *SubscriptionRecord
	Units                                []SubscriptionIntentUnit
}
type SubscriptionWantedDecision struct {
	UnitID        uuid.UUID
	ScopeExcluded bool
}
type SubscriptionCreateDecision struct {
	Command CreateSubscriptionCommand
	Wanted  []SubscriptionWantedDecision
}
type SubscriptionOwnedContext struct {
	Subscription SubscriptionRecord
	Wanted       []SubscriptionWanted
}

// A transaction exposes only the subscription intent boundary. Frozen material is
// locked and revalidated by the store; decisions belong to its caller.
type SubscriptionIntentTx interface {
	LockCreate(context.Context, SubscriptionCreateRequest) (SubscriptionCreateContext, error)
	ApplyCreate(context.Context, SubscriptionCreateDecision) (*SubscriptionRecord, []*SubscriptionWanted, error)
	LockOwned(context.Context, *Actor, uuid.UUID) (SubscriptionOwnedContext, error)
	ApplyState(context.Context, string) (*SubscriptionRecord, error)
	ApplySkipped(context.Context, uuid.UUID, bool) error
}
type SubscriptionIntentUoW interface {
	Within(context.Context, func(SubscriptionIntentTx) error) error
}
type SubscriptionMediaRepository interface {
	EnsureSubscriptionMedia(context.Context, SubscriptionMediaInput, []SubscriptionMediaUnitSpec) (uuid.UUID, error)
}
type SubscriptionDetailReader interface {
	ReadSubscriptionDetail(context.Context, *Actor, uuid.UUID) (*SubscriptionRecord, []*SubscriptionWanted, error)
}
