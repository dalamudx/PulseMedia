package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// WatchObserver narrows directory hints to the ordinary observation contract.
type WatchObserver struct{ repo ports.DownloadWatchObserver }

func NewWatchObserver(repo ports.DownloadWatchObserver) *WatchObserver {
	return &WatchObserver{repo: repo}
}

func (s *WatchObserver) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if preparer, ok := s.repo.(ports.DownloadObservationPreparer); ok {
		return ObserveDownloadWatchTarget(ctx, preparer, target)
	}
	return s.repo.ObserveDownloadWatchTarget(ctx, target)
}
