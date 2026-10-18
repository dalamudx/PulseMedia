package ports

import (
	"context"
	"github.com/google/uuid"
)

const CompleteTVTrackingJobKind = "complete_tv_coverage_tracking"

// Dedicated tracking authority cannot send, organize, or release protection.
// A new leased job must explicitly bind the original task and operation.
type CompleteTVTrackingRequest = CompleteTVObservationRequest
type CompleteTVTrackingSnapshot = CompleteTVObservationSnapshot

type CompleteTVTrackingObservation struct {
	Complete      bool
	ClientID      uuid.UUID
	ClientStamp   string
	Before, After []*TorrentInfo
	Files         []TorrentFile
	// Sealed read-only export binds the complete file set to the original infohash.
	Metainfo *CompleteTVMetainfoProof
}

type CompleteTVTrackingReceipt struct {
	TaskID         uuid.UUID     `json:"task_id"`
	State          string        `json:"state"`
	ReviewRequired bool          `json:"review_required"`
	EvidenceStamp  string        `json:"evidence_stamp"`
	Files          []TorrentFile `json:"files,omitempty"`
}

type CompleteTVTrackingRepository interface {
	LoadCompleteTVTracking(context.Context, CompleteTVTrackingRequest) (*CompleteTVTrackingSnapshot, error)
	CommitCompleteTVTracking(context.Context, CompleteTVTrackingSnapshot, CompleteTVTrackingObservation) (*CompleteTVTrackingReceipt, error)
}

// Explicit native read-only capability: both lookups and inventory are bounded,
// complete successful responses; nil is unavailable, empty is proven no-result.
type CompleteTVTrackingClient interface {
	ListTorrents(context.Context, string) ([]*TorrentInfo, error)
	GetTorrentFiles(context.Context, string) ([]TorrentFile, error)
	GetTorrentMetainfo(context.Context, string) ([]byte, error)
}
