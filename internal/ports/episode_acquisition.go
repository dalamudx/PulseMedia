package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrEpisodeAcquisitionConflict = errors.New("reviewed episode acquisition inputs changed or target is not eligible")

type AcquireReviewedEpisodeCommand struct {
	SubscriptionID     uuid.UUID `json:"subscription_id"`
	ReviewID           uuid.UUID `json:"review_id"`
	ExpectedInputStamp string    `json:"expected_input_stamp"`
	Confirmed          bool      `json:"confirmed"`
}
type EpisodeExecutionSnapshot struct {
	Kind              string                   `json:"kind"`
	ReviewID          uuid.UUID                `json:"review_id"`
	ReviewInputStamp  string                   `json:"review_input_stamp"`
	SubscriptionID    uuid.UUID                `json:"subscription_id"`
	IntentVersion     int64                    `json:"intent_version"`
	MediaID           uuid.UUID                `json:"media_id"`
	MediaType         string                   `json:"media_type"`
	MediaVersion      int64                    `json:"media_version"`
	UnitID            uuid.UUID                `json:"unit_id"`
	UnitKey           string                   `json:"unit_key"`
	UnitVersion       int64                    `json:"unit_version"`
	EpisodeID         int64                    `json:"episode_id"`
	MappingMode       string                   `json:"mapping_mode"`
	MappingRevision   int64                    `json:"mapping_revision"`
	MappingSourceHash string                   `json:"mapping_source_hash,omitempty"`
	MappingInputStamp string                   `json:"mapping_input_stamp,omitempty"`
	MappingValidUntil *time.Time               `json:"mapping_valid_until,omitempty"`
	SourceNumber      int                      `json:"source_number,omitempty"`
	MappingRows       []EpisodeGroupMappingRow `json:"mapping_rows,omitempty"`
	ParserVersion     string                   `json:"parser_version"`
	PolicyID          string                   `json:"policy_id"`
	PolicyRevision    int64                    `json:"policy_revision"`
	RootID            uuid.UUID                `json:"root_id"`
	RootGeneration    int64                    `json:"root_generation"`
	CandidateID       uuid.UUID                `json:"candidate_id"`
	IndexerID         uuid.UUID                `json:"indexer_id"`
	SourceVersion     int64                    `json:"source_version"`
	SourceStamp       string                   `json:"source_stamp"`
	ClientID          uuid.UUID                `json:"client_id"`
	ClientStamp       string                   `json:"client_stamp"`
	ActorID           uuid.UUID                `json:"actor_id"`
	AcquiredAt        time.Time                `json:"acquired_at"`
}
type EpisodeAcquisitionRepository interface {
	AcquireReviewedEpisode(context.Context, *Actor, AcquireReviewedEpisodeCommand, string) (*AcquireResult, error)
}
