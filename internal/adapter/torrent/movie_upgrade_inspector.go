package torrent

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (Inspector) InspectMovieUpgradeMetainfo(ctx context.Context, raw []byte, target media.MovieUpgradeTarget) (ports.MovieUpgradeMetainfoProof, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proof, err := InspectMovieUpgradeMetainfo(raw, target)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return proof, nil
}

var _ ports.MovieUpgradeMetainfoInspector = Inspector{}
