package ports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
	"time"
)

var ErrCompleteTVCoverageAcquisitionConflict = errors.New("complete TV coverage acquisition authority changed or target unavailable")

// AcquireCompleteTVCoverageCommand is separate from movie and single-episode authority.
// ExpectedInputStamp seals current observations; the immutable review alone never authorizes acquisition.
type AcquireCompleteTVCoverageCommand struct {
	SubscriptionID        uuid.UUID `json:"subscription_id"`
	ReviewID              uuid.UUID `json:"review_id"`
	ExpectedInputStamp    string    `json:"expected_input_stamp"`
	ExpectedMaterialStamp string    `json:"expected_material_stamp"`
	Confirmed             bool      `json:"confirmed"`
}

// CompleteTVCoverageExecutionSnapshot is the persisted authority for the dedicated
// full-set submission worker; it never authorizes the generic single-unit path.
type CompleteTVCoverageExecutionSnapshot struct {
	Kind             string                   `json:"kind"`
	ActorID          uuid.UUID                `json:"actor_id"`
	ReviewID         uuid.UUID                `json:"review_id"`
	ReviewInputStamp string                   `json:"review_input_stamp"`
	ObservationStamp string                   `json:"observation_stamp"`
	MaterialStamp    string                   `json:"material_stamp"`
	Material         json.RawMessage          `json:"material"`
	Facts            json.RawMessage          `json:"facts"`
	Units            []media.NumberingRow     `json:"units"`
	CompleteSeasons  []CompleteSeasonMetadata `json:"complete_seasons"`
	SubscriptionID   uuid.UUID                `json:"subscription_id"`
	IntentVersion    int64                    `json:"intent_version"`
	MediaID          uuid.UUID                `json:"media_id"`
	MediaVersion     int64                    `json:"media_version"`
	Mapping          EpisodeMappingState      `json:"mapping"`
	PolicyID         string                   `json:"policy_id"`
	PolicyRevision   int64                    `json:"policy_revision"`
	RootID           uuid.UUID                `json:"root_id"`
	RootGeneration   int64                    `json:"root_generation"`
	CandidateID      uuid.UUID                `json:"candidate_id"`
	CandidateSize    int64                    `json:"candidate_size"`
	SourceStamp      string                   `json:"source_stamp"`
	ClientID         uuid.UUID                `json:"client_id"`
	ClientStamp      string                   `json:"client_stamp"`
	AcquiredAt       time.Time                `json:"acquired_at"`
}

type CompleteTVCoverageAcquisitionRepository interface {
	AcquireCompleteTVCoverage(context.Context, *Actor, AcquireCompleteTVCoverageCommand, string) (*AcquireResult, error)
}
