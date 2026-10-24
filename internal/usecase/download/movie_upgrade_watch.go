package download

import (
	"context"
	"path/filepath"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// MovieUpgradeHintObserver selects only ordinary, already due tracking work.
// It never renews the original predecessor or grants a native execution permit.
type MovieUpgradeHintObserver struct {
	scheduler ports.MovieUpgradeTrackingHintScheduler
	files     ports.RootFilesystem
	wake      chan<- struct{}
}

func NewMovieUpgradeHintObserver(scheduler ports.MovieUpgradeTrackingHintScheduler, files ports.RootFilesystem, wake chan<- struct{}) *MovieUpgradeHintObserver {
	return &MovieUpgradeHintObserver{scheduler: scheduler, files: files, wake: wake}
}

func (s *MovieUpgradeHintObserver) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.scheduler == nil || s.files == nil {
		return nil, ports.ErrUnavailable
	}
	capture := target.MovieUpgradeHintCapture
	if target.Kind != "movie_upgrade_tracking_hint" || capture == nil {
		return nil, ports.ErrDownloadWatchStale
	}
	request := capture.MovieUpgradeTrackingHintRequest()
	if request.OperationID == uuid.Nil || request.TaskID == uuid.Nil || request.PriorObservationID == uuid.Nil || request.OperationID != target.OperationID || request.TaskID != target.TaskID || request.Binding != target.Binding || request.Binding.ClientID != target.ClientID || request.Binding.ClientStamp != target.ClientStamp || !request.Binding.ClientVersion.Equal(target.ClientVersion) {
		return nil, ports.ErrDownloadWatchStale
	}
	remaining := 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if request != capture.MovieUpgradeTrackingHintRequest() {
			return nil, ports.ErrDownloadWatchStale
		}
		// Validate the opaque issuer before interpreting any path as a root capability.
		if err := s.scheduler.ValidateMovieUpgradeTrackingHint(ctx, capture); err != nil {
			return nil, err
		}
		if err := s.probe(ctx, request); err != nil {
			return nil, err
		}
		result, err := s.scheduler.ScheduleMovieUpgradeTrackingHint(ctx, capture)
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if result == nil || result.RetryAfter < 0 {
			return nil, ports.ErrUnavailable
		}
		if result.Queued {
			if result.JobID == uuid.Nil || result.RetryAfter != 0 {
				return nil, ports.ErrUnavailable
			}
			select {
			case s.wake <- struct{}{}:
			default:
			}
			return nil, nil
		}
		if result.JobID != uuid.Nil {
			return nil, ports.ErrUnavailable
		}
		if result.RetryAfter == 0 {
			return nil, nil
		}
		if result.RetryAfter > remaining {
			return nil, ports.ErrUnavailable
		}
		remaining -= result.RetryAfter
		timer := time.NewTimer(result.RetryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *MovieUpgradeHintObserver) probe(ctx context.Context, request ports.MovieUpgradeTrackingHintRequest) error {
	b := request.Binding
	if !filepath.IsAbs(b.LocalRoot) || !filepath.IsAbs(b.RootPath) || b.RootIdentity.Device < 0 || b.RootIdentity.Inode <= 0 || request.SourceIdentity.Device < 0 || request.SourceIdentity.Inode <= 0 || b.MarkerFilename == "" || b.MarkerFilename == "." || b.MarkerFilename == ".." || filepath.Base(b.MarkerFilename) != b.MarkerFilename {
		return ports.ErrDownloadWatchStale
	}
	source, err := s.files.OpenRoot(ctx, b.LocalRoot)
	if err != nil {
		return movieUpgradeProbeError(ctx, err)
	}
	if source == nil {
		return ports.ErrUnavailable
	}
	defer source.Close()
	identity, err := source.Identity(ctx)
	if err != nil {
		return movieUpgradeProbeError(ctx, err)
	}
	if !movieUpgradeRootSame(identity, request.SourceIdentity) {
		return ports.ErrDownloadWatchStale
	}
	library := source
	if b.RootPath != b.LocalRoot {
		library, err = s.files.OpenRoot(ctx, b.RootPath)
		if err != nil {
			return movieUpgradeProbeError(ctx, err)
		}
		if library == nil {
			return ports.ErrUnavailable
		}
		defer library.Close()
	}
	identity, err = library.Identity(ctx)
	if err != nil {
		return movieUpgradeProbeError(ctx, err)
	}
	if !movieUpgradeRootSame(identity, b.RootIdentity) {
		return ports.ErrDownloadWatchStale
	}
	if _, err = library.Inspect(ctx, b.MarkerFilename); err != nil {
		return movieUpgradeProbeError(ctx, err)
	}
	return ctx.Err()
}

func movieUpgradeRootSame(a, b ports.FileIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode
}
func movieUpgradeProbeError(ctx context.Context, _ error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ports.ErrDownloadWatchStale
}
