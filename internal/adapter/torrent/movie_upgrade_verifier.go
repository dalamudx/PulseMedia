package torrent

import (
	"context"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MovieUpgradeMetainfoProof = (*MovieUpgradeMetainfoProof)(nil)
var _ ports.MovieUpgradeMetainfoVerifier = Inspector{}

// VerifyMovieUpgradeMetainfo accepts only the native private proof. A custom
// implementation reporting ValidateBinding=true cannot mint inspected evidence.
// Current database policy, authority and lease checks remain the UoW's duty.
func (Inspector) VerifyMovieUpgradeMetainfo(ctx context.Context, claim ports.MovieUpgradeMetainfoProof, target media.MovieUpgradeTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, ok := claim.(*MovieUpgradeMetainfoProof)
	if !ok || !p.ValidateBinding() {
		return ports.ErrMovieUpgradeMetainfoReview
	}
	actual := p.target
	if actual.MediaID != target.MediaID || actual.UnitID != target.UnitID ||
		actual.MediaVersion != target.MediaVersion || actual.UnitVersion != target.UnitVersion ||
		actual.MediaType != target.MediaType || actual.UnitKey != target.UnitKey ||
		actual.OriginalTitle != target.OriginalTitle || actual.Year != target.Year ||
		actual.ParserVersion != target.ParserVersion ||
		!slices.Equal(actual.Identities, target.Identities) || !slices.Equal(actual.Aliases, target.Aliases) {
		return ports.ErrMovieUpgradeMetainfoReview
	}
	return ctx.Err()
}

// InspectMovieUpgradeTrackingMetainfo decodes a bounded native export without
// storing or logging its bytes, which may include a private tracker passkey.
func (i Inspector) InspectMovieUpgradeTrackingMetainfo(ctx context.Context, raw []byte, target media.MovieUpgradeTarget) (ports.MovieUpgradeMetainfoProof, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proof, err := InspectMovieUpgradeMetainfo(raw, target)
	if err != nil {
		return nil, err
	}
	if err = i.VerifyMovieUpgradeMetainfo(ctx, proof, target); err != nil {
		return nil, err
	}
	return proof, nil
}

var _ ports.MovieUpgradeTrackingMetainfoInspector = Inspector{}
