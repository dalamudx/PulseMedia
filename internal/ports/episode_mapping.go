package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	subscriptiondomain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/google/uuid"
	"time"
)

var ErrInvalidEpisodeMapping = errors.New("invalid episode mapping command")

type ManualEpisodeMappingEntry struct {
	SourceNumber int       `json:"source_number"`
	UnitID       uuid.UUID `json:"unit_id"`
}
type EpisodeMappingDraft struct {
	Mode    string                      `json:"mode"`
	GroupID string                      `json:"group_id,omitempty"`
	PartID  string                      `json:"part_id,omitempty"`
	Entries []ManualEpisodeMappingEntry `json:"entries,omitempty"`
}
type EpisodeMappingPreview struct {
	SubscriptionID  uuid.UUID                `json:"subscription_id"`
	MediaID         uuid.UUID                `json:"media_id"`
	Mode            string                   `json:"mode"`
	GroupID         string                   `json:"group_id"`
	PartID          string                   `json:"part_id"`
	MappingRevision int64                    `json:"mapping_revision"`
	InputStamp      string                   `json:"input_stamp"`
	SourceHash      string                   `json:"source_hash"`
	CanPublish      bool                     `json:"can_publish"`
	HoldReason      string                   `json:"hold_reason"`
	Rows            []EpisodeGroupMappingRow `json:"rows"`
	Source          *TVEpisodeGroupSnapshot  `json:"source,omitempty"`
}
type EpisodeMappingCommand struct {
	Draft              EpisodeMappingDraft `json:"draft"`
	ExpectedInputStamp string              `json:"expected_input_stamp"`
	ExpectedSourceHash string              `json:"expected_source_hash"`
	Confirmed          bool                `json:"confirmed"`
}
type EpisodeMappingReceipt struct {
	SubscriptionID  uuid.UUID  `json:"subscription_id"`
	MappingRevision int64      `json:"mapping_revision"`
	IntentVersion   int64      `json:"intent_version"`
	Mode            string     `json:"mode"`
	SourceHash      string     `json:"source_hash"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
}
type EpisodeMappingState struct {
	TMDBID          int64                    `json:"-"` // Negative identity evidence only; never execution permission.
	MediaID         uuid.UUID                `json:"media_id"`
	InputStamp      string                   `json:"input_stamp"`
	IntentVersion   int64                    `json:"intent_version"`
	ObservedAt      time.Time                `json:"observed_at"`
	SubscriptionID  uuid.UUID                `json:"subscription_id"`
	MappingRevision int64                    `json:"mapping_revision"`
	Mode            string                   `json:"mode"`
	Current         bool                     `json:"current"`
	HoldReason      string                   `json:"hold_reason"`
	SourceHash      string                   `json:"source_hash"`
	Rows            []EpisodeGroupMappingRow `json:"rows"`
	ValidUntil      *time.Time               `json:"valid_until,omitempty"`
}
type EpisodeCandidateObservation struct {
	Mapping                          EpisodeMappingState
	CanonicalUnits                   []EpisodeSourceUnit
	Target                           subscriptiondomain.Snapshot
	Quality                          quality.Profile
	ParentTitle, ParentOriginalTitle string
	ParentYear                       int
	CandidateID, IndexerID           uuid.UUID
	Name, InputStamp, HoldReason     string
	SourceVersion                    int64
	Current                          bool
}

type EpisodeMappingRepository interface {
	EpisodeSourceRepository
	ReadEpisodeMappingReceipt(context.Context, *Actor, uuid.UUID, string, string) (*EpisodeMappingReceipt, error)
	PublishEpisodeMapping(context.Context, *Actor, EpisodeMappingPreview, string, string) (*EpisodeMappingReceipt, error)
	ReadEpisodeMapping(context.Context, *Actor, uuid.UUID) (*EpisodeMappingState, error)
	ReadEpisodeCandidate(context.Context, *Actor, uuid.UUID, uuid.UUID) (*EpisodeCandidateObservation, error)
}
