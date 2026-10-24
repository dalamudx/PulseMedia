package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// A durable enqueue may wake only the dedicated tracking consumer. Nil result
// deliberately cannot trigger the generic organization's success branch.
type completeTVHintWakeObserver struct {
	delegate ports.DownloadWatchObserver
	wake     chan<- struct{}
}

func (s completeTVHintWakeObserver) ObserveDownloadWatchTarget(ctx context.Context, t ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	if s.delegate == nil {
		return nil, ports.ErrUnavailable
	}
	result, err := s.delegate.ObserveDownloadWatchTarget(ctx, t)
	if err == nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return result, err
}
