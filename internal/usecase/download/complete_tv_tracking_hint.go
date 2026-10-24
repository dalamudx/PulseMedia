package download

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// CompleteTVHintObserver only enqueues the dedicated tracking successor. Actual
// native observation belongs to the existing owned COMPLETE-TV consumer.
type CompleteTVHintObserver struct {
	scheduler ports.CompleteTVTrackingHintScheduler
}

func NewCompleteTVHintObserver(scheduler ports.CompleteTVTrackingHintScheduler) *CompleteTVHintObserver {
	return &CompleteTVHintObserver{scheduler: scheduler}
}

func (s *CompleteTVHintObserver) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.scheduler == nil {
		return nil, ports.ErrUnavailable
	}
	if target.Kind != "complete_tv_tracking_hint" || target.TrackingHintCapture == nil {
		return nil, ports.ErrDownloadWatchStale
	}
	request := target.TrackingHintCapture.TrackingHintRequest()
	if request.Binding != target.Binding || request.TaskID != target.TaskID || request.OperationID != target.OperationID || request.Binding.ClientID != target.ClientID || request.Binding.ClientStamp != target.ClientStamp || !request.Binding.ClientVersion.Equal(target.ClientVersion) {
		return nil, ports.ErrDownloadWatchStale
	}
	receipt, err := s.scheduler.ScheduleCompleteTVTrackingHint(ctx, target.TrackingHintCapture)
	if err != nil {
		return nil, err
	}
	if receipt == nil || receipt.JobID == uuid.Nil || receipt.JobID == request.PriorJobID || receipt.AcceptedAt.IsZero() || receipt.HintID != request.HintID || receipt.TaskID != request.TaskID || receipt.OperationID != request.OperationID || receipt.PriorJobID != request.PriorJobID {
		return nil, ports.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Enqueueing is not an observation result or an organization completion.
	return nil, nil
}
