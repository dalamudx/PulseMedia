package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
	"time"
)

// UpgradePreviewVersion is an observation fence for fresh assessment only.
// InputStamp covers all source, mapping, facts, projection and policy inputs.
// It is deliberately not an executable seal.
type UpgradePreviewVersion struct {
	ActorID        uuid.UUID `json:"actor_id"`
	SubscriptionID uuid.UUID `json:"subscription_id"`
	MediaID        uuid.UUID `json:"media_id"`
	UnitID         uuid.UUID `json:"unit_id"`
	RootID         uuid.UUID `json:"root_id"`
	IntentVersion  int64     `json:"intent_version"`
	PolicyID       string    `json:"policy_id"`
	PolicyRevision int64     `json:"policy_revision"`
	InputStamp     string    `json:"input_stamp"`
}
type UpgradePreviewRequest struct {
	SubscriptionID uuid.UUID
	CurrentFileID  uuid.UUID
	CandidateID    uuid.UUID
	Expected       *UpgradePreviewVersion
}
type UpgradePreviewSnapshot struct {
	Mode                                                UpgradeMode `json:"-"`
	Version                                             UpgradePreviewVersion
	ObservedAt                                          time.Time
	CurrentFileID, CurrentFactID, CandidateID, SourceID uuid.UUID
	Current                                             quality.Profile
	Candidate                                           media.CandidateRuleInput
	HoldReason                                          string
	ExpectedClientDigest                                string `json:"-"`
}
type UpgradePreview struct {
	Status               string                `json:"status"`
	ExecutionAllowed     bool                  `json:"execution_allowed"`
	Comparable           bool                  `json:"comparable"`
	Improvement          bool                  `json:"improvement"`
	Reason               string                `json:"reason"`
	CurrentFileID        uuid.UUID             `json:"current_file_id"`
	CurrentFactID        uuid.UUID             `json:"current_fact_id"`
	CandidateID          uuid.UUID             `json:"candidate_id"`
	SourceID             uuid.UUID             `json:"source_id"`
	Version              UpgradePreviewVersion `json:"version"`
	ObservedAt           time.Time             `json:"observed_at"`
	CurrentScore         *int                  `json:"current_score,omitempty"`
	CandidateScore       *int                  `json:"candidate_score,omitempty"`
	ExpectedClientDigest string                `json:"expected_client_digest,omitempty"`
}
type UpgradePreviewRepository interface {
	ReadUpgradePreview(context.Context, *Actor, UpgradePreviewRequest) (*UpgradePreviewSnapshot, error)
}
