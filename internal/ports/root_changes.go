package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var (
	ErrFileWatchUnavailable  = errors.New("root change notifications unavailable")
	ErrFileWatchCoverageLost = errors.New("root change notification coverage lost; controlled reconciliation required")
)

// RootChangeHint prompts observation; it never proves completion, identity,
// ownership, absence, or permission to organize or publish a file.
type RootChangeHint struct {
	RelativePath string
	Kind         RootChangeKind
	Metadata     RootChangeMetadata
}

// RootChangeMetadata is assigned by the owned App forwarder, never the native
// source. A zero value preserves ordinary observation behavior.
type RootChangeMetadata struct {
	EventID, InstanceID    uuid.UUID
	Sequence               int64
	ReceivedAt, ManifestAt time.Time
	SourceIdentity         FileIdentity
}
type RootChangeKind string

const (
	RootChangeCloseWrite RootChangeKind = "close_write"
	RootChangeMovedTo    RootChangeKind = "moved_to"
	RootChangeRescan     RootChangeKind = "rescan"
)

// RootWatchRequest comes from validated local configuration, never from an
// observed downloader path. FrozenIdentity binds device and inode only.
type RootWatchRequest struct {
	Root           TransferRoot
	ConfiguredPath string
	FrozenIdentity FileIdentity
}

// WatchRoot synchronously owns native watches until return. The caller retains
// Root and owns the bounded output channel, which must remain open until return.
// Saturation and coverage discontinuity fail explicitly; neither means missing
// files. Watching does not occupy the exclusive slots needed for transfers.
type RootChangeFilesystem interface {
	RootFilesystem
	WatchRoot(context.Context, RootWatchRequest, chan<- RootChangeHint) error
}
