package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrCompleteTVObservationStale = errors.New("complete TV observation changed; protection retained")

const CompleteTVObservationJobKind = "complete_tv_coverage_observation"

// CompleteTVObservationRequest uses a NEW observation lease, never the send lease.
// Its job payload contains the explicit original task_id and operation_id.
type CompleteTVObservationRequest struct {
	Lease               JobLease
	TaskID, OperationID uuid.UUID
}

// CompleteTVObservationSnapshot is read authority only. Seal binds the original
// immutable preparation, task, coverage, observed client configuration and ALL
// protection rows. It contains no credential, URL, or metainfo bytes.
// The caller must retain the sender's operation physical lock and shared client
// budget through native observation and commit. No network runs in this UoW.
type CompleteTVObservationSnapshot struct {
	Request                                       CompleteTVObservationRequest
	ClientID                                      uuid.UUID
	ClientStamp, InfoHash, OperationTag, SavePath string
	TotalBytes                                    int64
	Seal                                          string
	PhysicalPermit                                uuid.UUID
}

// CompleteTVRemoteObservation must describe a successful, COMPLETE native lookup.
// Complete=false (offline, transport, truncated or indeterminate response) is
// unavailable, never evidence of absence. Client identity is the actual adapter
// configuration used for that lookup, not merely the desired configuration.
type CompleteTVRemoteObservation struct {
	Complete    bool
	ClientID    uuid.UUID
	ClientStamp string
	Matches     []CompleteTVRemoteMatch
}
type CompleteTVRemoteMatch struct {
	InfoHash, OperationTag, SavePath string
	TotalBytes                       int64
}
type CompleteTVObservationReceipt struct {
	TaskID         uuid.UUID `json:"task_id"`
	State          string    `json:"state"`
	ReviewRequired bool      `json:"review_required"`
}

// CompleteTVObservationRepository cannot acquire, prepare, Add, organize or
// release protection. Loading commits its short transaction before native I/O.
type CompleteTVObservationRepository interface {
	LoadCompleteTVObservation(context.Context, CompleteTVObservationRequest) (*CompleteTVObservationSnapshot, error)
	CommitCompleteTVObservation(context.Context, CompleteTVObservationSnapshot, CompleteTVRemoteObservation) (*CompleteTVObservationReceipt, error)
}
