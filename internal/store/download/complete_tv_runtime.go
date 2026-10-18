package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var _ ports.CompleteTVExecutor = (*Repository)(nil)

func (r *Repository) ObserveCompleteTVTracking(ctx context.Context, repository ports.CompleteTVTrackingRepository, inspector ports.TorrentMetainfoInspector, request ports.CompleteTVTrackingRequest) (*ports.CompleteTVTrackingReceipt, error) {
	return r.execution.ObserveCompleteTVTracking(ctx, repository, inspector, request)
}

func (r *Repository) SetCompleteTVServices(preparation ports.CompleteTVSubmissionRepository, results ports.CompleteTVSubmissionResultRepository, fetcher ports.CompleteTVTorrentFetcher, inspector ports.TorrentMetainfoInspector) {
	r.execution.SetCompleteTVServices(preparation, results, fetcher, inspector)
}
func (r *Repository) SendCompleteTV(ctx context.Context, lease ports.JobLease, operation uuid.UUID) (*ports.CompleteTVSubmissionResultReceipt, error) {
	return r.execution.SendCompleteTV(ctx, lease, operation)
}
func (r *Repository) ObserveCompleteTV(ctx context.Context, repository ports.CompleteTVObservationRepository, request ports.CompleteTVObservationRequest) (*ports.CompleteTVObservationReceipt, error) {
	return r.execution.ObserveCompleteTV(ctx, repository, request)
}
