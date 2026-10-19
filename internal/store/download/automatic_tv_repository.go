package download

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.AutomaticTVSendJobExecutor = (*Repository)(nil)

func (r *Repository) SetAutomaticTVServices(repository ports.AutomaticTVSubmissionRepository, fetcher ports.AutomaticTVTorrentFetcher, inspector ports.TorrentMetainfoInspector) {
	r.execution.SetAutomaticTVServices(repository, fetcher, inspector)
}

func (r *Repository) ExecuteAutomaticTVSendJob(ctx context.Context, request ports.AutomaticTVSendRequest) error {
	return r.execution.ExecuteAutomaticTVSendJob(ctx, request)
}

func (r *Repository) ObserveAutomaticTV(ctx context.Context, repository ports.AutomaticTVObservationRepository, request ports.AutomaticTVObservationRequest) (*ports.AutomaticTVObservationReceipt, error) {
	return r.execution.ObserveAutomaticTV(ctx, repository, request)
}
