package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// ReceivedAt is the owned watcher's local receipt time, measured by Clock.
// SourceIdentity describes LocalRoot, independently of the library baseline.
// PriorJobID is captured on first resolution and MUST survive every retry.
type CompleteTVTrackingHintRequest struct {
	OperationID, TaskID, PriorJobID uuid.UUID
	HintID, InstanceID              uuid.UUID
	Sequence                        int64
	Hint                            RootChangeHint
	Binding                         DownloadWatchBinding
	SourceIdentity                  FileIdentity
	ReceivedAt                      time.Time
}

// Capture is an opaque repository-issued value, not caller-supplied authority.
type CompleteTVTrackingHintCapture interface {
	TrackingHintRequest() CompleteTVTrackingHintRequest
}

type CompleteTVTrackingHintReceipt struct {
	JobID       uuid.UUID `json:"job_id"`
	TaskID      uuid.UUID `json:"task_id"`
	OperationID uuid.UUID `json:"operation_id"`
	PriorJobID  uuid.UUID `json:"prior_job_id"`
	HintID      uuid.UUID `json:"hint_id"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

// Evidence is safe to carry outside the transaction for native root reprobes.
// Its digest must be included in the original tracking snapshot Seal.
type CompleteTVTrackingHintEvidence struct {
	// Digest is the same value returned by the transaction validators.
	Digest                                                            string
	Receipt                                                           CompleteTVTrackingHintReceipt
	InstanceID                                                        uuid.UUID
	Sequence                                                          int64
	Hint                                                              RootChangeHint
	Binding                                                           DownloadWatchBinding
	SourceIdentity                                                    FileIdentity
	PriorDigest, AuthorityDigest, PreparationDigest, ProtectionDigest string
	RequestDigest                                                     string
}

type CompleteTVTrackingHintScheduler interface {
	ResolveCompleteTVTrackingHint(context.Context, CompleteTVTrackingHintRequest, Clock) (CompleteTVTrackingHintCapture, error)
	ScheduleCompleteTVTrackingHint(context.Context, CompleteTVTrackingHintCapture) (*CompleteTVTrackingHintReceipt, error)
	ReadCompleteTVTrackingHint(context.Context, uuid.UUID) (*CompleteTVTrackingHintEvidence, error)
}

// CompleteTVTrackingHintReader is optional. Call it while the existing tracking
// manager owns its physical permit, before/after native reads. A nil evidence
// leaves periodic jobs on their original path; no pointer enters the snapshot.
type CompleteTVTrackingHintReader interface {
	ReadCompleteTVTrackingHintForLease(context.Context, JobLease) (*CompleteTVTrackingHintEvidence, error)
}
