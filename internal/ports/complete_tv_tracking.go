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
	// Sealed native export for protocols that support it. Transmission instead
	// uses the full manifest from the original immutable submission preparation.
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

// CompleteTVManifestTrackingClient exposes complete bounded native identity and
// inventory reads. It makes no claim that the native protocol can export metainfo.
// nil is unavailable; an allocated empty list is a complete no-result response.
type CompleteTVManifestTrackingClient interface {
	ListTorrents(context.Context, string) ([]*TorrentInfo, error)
	GetTorrentFiles(context.Context, string) ([]TorrentFile, error)
}

// CompleteTVTrackingClient additionally exports the native original info bytes.
// Protocols without export must use original persisted preparation evidence.
type CompleteTVTrackingClient interface {
	CompleteTVManifestTrackingClient
	GetTorrentMetainfo(context.Context, string) ([]byte, error)
}
