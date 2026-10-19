package ports

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

const AutomaticTVObservationJobKind = "automatic_tv_observation"

var ErrAutomaticTVObservationStale = errors.New("automatic TV observation authority changed; protection retained")

// Observation has a fresh lease and a read-only permit. It cannot finish or
// take over the original physical send attempt, authorize Add or release a lane.
type AutomaticTVObservationRequest struct {
	Lease            JobLease  `json:"-"`
	TaskID           uuid.UUID `json:"-"`
	OperationID      uuid.UUID `json:"-"`
	PhysicalPermitID uuid.UUID `json:"-"`
}

type AutomaticTVObservationSnapshot struct {
	Request               AutomaticTVObservationRequest `json:"-"`
	Authority             AutomaticTVExecutionAuthority `json:"-"`
	SendPhysicalAttemptID uuid.UUID                     `json:"-"`
	ClientID              uuid.UUID                     `json:"-"`
	ClientStamp           string                        `json:"-"`
	InfoHash              string                        `json:"-"`
	OperationTag          string                        `json:"-"`
	SavePath              string                        `json:"-"`
	TotalBytes            int64                         `json:"-"`
	MetainfoSHA256        [32]byte                      `json:"-"`
	Files                 []media.TVTorrentFile         `json:"-"`
	Video                 media.TVTorrentUnitFile       `json:"-"`
	Seal                  string                        `json:"-"`
}

// Every required native field was present and validated before constructing a
// task. VerifiedBytes is a trusted native integer, never a progress estimate.
type AutomaticTVRemoteTask struct {
	InfoHash      string
	SavePath      string
	ContentPath   string
	State         string
	Tags          []string
	TotalBytes    int64
	VerifiedBytes int64
	Complete      bool
	// Display telemetry never changes sealed task or completion evidence.
	DownloadRateBytesPerSecond *int64 `json:"-"`
}

// The complete inventory includes unwanted files and zero-byte attachments.
// CompletedBytes is the exact native integer for this file.
type AutomaticTVRemoteFile struct {
	Index          int
	Path           string
	Size           int64
	CompletedBytes int64
	Wanted         bool
}

// Both methods describe COMPLETE successful bounded native responses. A nil
// slice is unavailable; a non-nil empty task slice is a proven lookup result.
// A read-only adapter exposes no Add, Remove or cancellation method.
type AutomaticTVReadClient interface {
	ListAutomaticTVTasks(context.Context, string) ([]AutomaticTVRemoteTask, error)
	GetAutomaticTVFiles(context.Context, string) ([]AutomaticTVRemoteFile, error)
}

type AutomaticTVDownloadObservation struct {
	Complete    bool
	ClientID    uuid.UUID
	ClientStamp string
	Before      []AutomaticTVRemoteTask
	After       []AutomaticTVRemoteTask
	Files       []AutomaticTVRemoteFile
}

type AutomaticTVObservationReceipt struct {
	TaskID         uuid.UUID `json:"task_id"`
	State          string    `json:"state"`
	ReviewRequired bool      `json:"review_required"`
	Complete       bool      `json:"complete"`
	EvidenceStamp  string    `json:"evidence_stamp"`
}

// Each transaction ends before native I/O. Commit publishes only observation
// evidence; current business eligibility independently gates organization.
type AutomaticTVObservationRepository interface {
	LoadAutomaticTVObservation(context.Context, AutomaticTVObservationRequest) (*AutomaticTVObservationSnapshot, error)
	CommitAutomaticTVObservation(context.Context, AutomaticTVObservationSnapshot, AutomaticTVDownloadObservation) (*AutomaticTVObservationReceipt, error)
	ScheduleAutomaticTVObservations(context.Context, int32) (int, error)
	RecoverAutomaticTVObservations(context.Context, int32) (int, error)
}
