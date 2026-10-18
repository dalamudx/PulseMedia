package ports

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

var ErrMovieUpgradeMetainfoReview = errors.New("movie upgrade metainfo requires manual review")

// MovieUpgradeMetainfoProof exposes owned, immutable file evidence. Getter results
// are copies. The interface itself and ValidateBinding cannot prove native issuance:
// a repository must use its trusted injected verifier before trusting this view.
// Proof of file coverage never authorizes submission or retirement of an old file.
type MovieUpgradeMetainfoProof interface {
	ValidateBinding() bool
	Bytes() []byte
	SHA256() [32]byte
	InfoHash() string
	TotalBytes() int64
	Files() []media.TVTorrentFile
	Target() media.MovieUpgradeTarget
	Video() media.MovieUpgradeUnitFile
}

// MovieUpgradeMetainfoVerifier authenticates native issuance, byte ownership and
// exact frozen target binding without network or filesystem I/O. It is an explicit
// trusted dependency; caller-provided proof implementations must be rejected.
type MovieUpgradeMetainfoVerifier interface {
	VerifyMovieUpgradeMetainfo(context.Context, MovieUpgradeMetainfoProof, media.MovieUpgradeTarget) error
}
