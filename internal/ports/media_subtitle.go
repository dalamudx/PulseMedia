package ports

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

const MediaSubtitleJobKind = "media_subtitle"
const MaxSubtitleBytes = media.MaxChineseSubtitleBytes
const MinSubtitleVideoBytes = media.MinShooterVideoBytes

var ErrSubtitleVideoTooSmall = errors.New("video is too small for subtitle hashing")

// SubtitleVideoHash is provider lookup evidence only, never file authority.
// It is computed from one already verified file descriptor under the physical slot.
type SubtitleVideoHash struct {
	Hash      string `json:"hash"`
	SizeBytes int64  `json:"size_bytes"`
}

// A nil body with a nil error means a definite empty lookup. Unknown upstream
// results must return an error. No provider may open or write a local file.
type ChineseSubtitleProvider interface {
	FindChineseSubtitle(context.Context, SubtitleVideoHash) ([]byte, error)
}

// The caller retains LockRoots through synchronous hashing, network I/O,
// publication and cleanup, including cancellation. No absolute source is accepted.
type MediaSubtitleFilesystem interface {
	RootFilesystem
	InspectSidecarDirectory(context.Context, TransferRoot, string) (SidecarDirectoryIdentity, error)
	HashSubtitleVideo(context.Context, TransferRoot, string, FileIdentity) (SubtitleVideoHash, error)
	PublishSubtitle(context.Context, TransferRoot, SidecarRequest) (SidecarResult, error)
}

// Outcome is not_found, too_small, or observed. Observed records the actual
// publication disposition; uncertain or unsafe entries require manual review.
type MediaSubtitleCompletion struct {
	Outcome     string            `json:"outcome"`
	VideoHash   SubtitleVideoHash `json:"video_hash"`
	Name        string            `json:"name"`
	Publication SidecarResult     `json:"publication"`
}

// Reuses the committed imported-file snapshot, but has a separate job kind,
// completion and audit; a metadata job cannot authorize subtitle execution.
type MediaSubtitleRepository interface {
	LoadSubtitle(context.Context, JobLease) (MediaSidecarSnapshot, error)
	ValidateSubtitle(context.Context, JobLease, MediaSidecarSnapshot) error
	CompleteSubtitle(context.Context, JobLease, MediaSidecarSnapshot, MediaSubtitleCompletion) error
}
