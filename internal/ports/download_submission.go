package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrDownloadDemandChanged = errors.New("download demand no longer authorizes submission")

type DownloadDemandFence struct {
	PolicyID             string     `json:"policy_id"`
	PolicyRevision       int64      `json:"policy_revision"`
	SubscriptionID       *uuid.UUID `json:"subscription_id,omitempty"`
	IntentVersion        int64      `json:"intent_version"`
	RootGeneration       int64      `json:"root_generation"`
	UnitMetadataVersion  int64      `json:"unit_metadata_version"`
	MediaMetadataVersion int64      `json:"media_metadata_version"`
	CandidateID          uuid.UUID  `json:"candidate_id"`
	RootID               uuid.UUID  `json:"root_id"`
	UnitID               uuid.UUID  `json:"unit_id"`
	MediaID              uuid.UUID  `json:"media_id"`
	ActorID              string     `json:"actor_id"`
	ActorType            ActorType  `json:"actor_type"`
	SourceStamp          string     `json:"source_stamp"`
}
type DownloadSubmissionCommand struct {
	Lease                                      JobLease
	TaskID, OperationID, ClientID, CandidateID uuid.UUID
	InfoHash, SavePath                         string
	CipherHash                                 [32]byte
}
type DownloadSubmissionReceipt struct {
	TaskID         uuid.UUID
	AlreadyExisted bool
	State          string
	Declined       bool
}
type DownloadRequirementRepository interface {
	AttachRequirement(context.Context, *Actor, uuid.UUID, uuid.UUID, uuid.UUID) error
	ReleaseRequirement(context.Context, *Actor, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error)
}
type DownloadSubmissionRepository interface {
	PrepareSubmission(context.Context, DownloadSubmissionCommand) (*DownloadSubmissionReceipt, error)
	CompleteSubmission(context.Context, JobLease, uuid.UUID, bool) (*DownloadSubmissionReceipt, error)
}
