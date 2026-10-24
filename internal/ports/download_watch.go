package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrDownloadWatchStale = errors.New("download watch binding is no longer current")

// Bindings contain only safe configuration projections. Stamps detect changes;
// they are never authority supplied by a caller. Repositories reconstruct them.
type DownloadWatchBinding struct {
	Stamp          string
	ClientID       uuid.UUID
	ClientVersion  time.Time
	ClientStamp    string
	SourceKind     string
	RemotePrefix   string
	LocalRoot      string
	RootID         uuid.UUID
	Generation     int64
	RootPath       string
	MediaType      string
	MarkerFilename string
	BaselineScanID uuid.UUID
	RootIdentity   FileIdentity
	ScopeID        uuid.UUID    `json:",omitzero"`
	ScopeVersion   int64        `json:",omitzero"`
	SourceIdentity FileIdentity `json:",omitzero"`
}

type DownloadWatchSource struct {
	Binding    DownloadWatchBinding
	ManifestAt time.Time
}
type DownloadWatchCoverageIssue struct {
	ClientID uuid.UUID
	RootID   uuid.UUID
	Reason   string
}
type DownloadWatchSourceQuery struct {
	Cursor string
	Limit  int
}
type DownloadWatchSourcePage struct {
	Sources    []DownloadWatchSource
	NextCursor string
	Complete   bool
	Issues     []DownloadWatchCoverageIssue
}

// A target carries ordinary observation or an opaque dedicated tracking capture.
// Consumers dispatch by Kind; neither path permits Add/Remove or organization.
type DownloadWatchTarget struct {
	Binding                 DownloadWatchBinding
	TaskID                  uuid.UUID
	OperationID             uuid.UUID
	ClientID                uuid.UUID
	ClientVersion           time.Time
	ClientStamp             string
	TaskStamp               string
	Kind                    string
	TrackingHintCapture     CompleteTVTrackingHintCapture
	AutomaticTVHintCapture  AutomaticTVObservationHintCapture
	MovieUpgradeHintCapture MovieUpgradeTrackingHintCapture
	ScopeID                 uuid.UUID `json:",omitzero"`
	ScopeVersion            int64     `json:",omitzero"`
}
type DownloadWatchTargetQuery struct {
	Cursor string
	Limit  int
}
type DownloadWatchTargetPage struct {
	Targets    []DownloadWatchTarget
	NextCursor string
	Complete   bool
}

type DownloadWatchRepository interface {
	ListDownloadWatchSources(context.Context, DownloadWatchSourceQuery) (DownloadWatchSourcePage, error)
	ResolveDownloadWatchTargets(context.Context, DownloadWatchBinding, RootChangeHint, DownloadWatchTargetQuery) (DownloadWatchTargetPage, error)
}
type DownloadWatchObserver interface {
	ObserveDownloadWatchTarget(context.Context, DownloadWatchTarget) (*DownloadExecutionTask, error)
}
