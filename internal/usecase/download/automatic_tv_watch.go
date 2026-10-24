package download

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// AutomaticTVHintObserver selects only already eligible normal observation work.
// The repository alone decides when the original two-second gate is satisfied.
type AutomaticTVHintObserver struct {
	scheduler ports.AutomaticTVObservationHintScheduler
}

func NewAutomaticTVHintObserver(scheduler ports.AutomaticTVObservationHintScheduler) *AutomaticTVHintObserver {
	return &AutomaticTVHintObserver{scheduler: scheduler}
}

func (s *AutomaticTVHintObserver) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.scheduler == nil {
		return nil, ports.ErrUnavailable
	}
	capture := target.AutomaticTVHintCapture
	if target.Kind != "automatic_tv_observation_hint" || capture == nil {
		return nil, ports.ErrDownloadWatchStale
	}
	request := capture.AutomaticTVObservationHintRequest()
	if request.OperationID != target.OperationID || request.TaskID != target.TaskID || request.Binding != target.Binding || request.Binding.ClientID != target.ClientID || request.Binding.ClientStamp != target.ClientStamp || !request.Binding.ClientVersion.Equal(target.ClientVersion) {
		return nil, ports.ErrDownloadWatchStale
	}
	waitBudget := 2 * time.Second
	for {
		result, err := s.scheduler.ScheduleAutomaticTVObservationHint(ctx, capture)
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
			if result.JobID == uuid.Nil || result.JobID == request.PriorJobID || result.RetryAfter != 0 {
				return nil, ports.ErrUnavailable
			}
			return nil, nil
		}
		if result.RetryAfter == 0 {
			return nil, nil
		}
		if result.JobID != uuid.Nil || result.RetryAfter > waitBudget {
			return nil, ports.ErrUnavailable
		}
		waitBudget -= result.RetryAfter
		timer := time.NewTimer(result.RetryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		// Keep this exact predecessor and receipt. Waiting never resolves a new one.
	}
}
