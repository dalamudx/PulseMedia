package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func (a *App) runOrganizationWorker(ctx context.Context, queue *jobs.Queue, svc *transfer.Service, reuse ...*transfer.LibraryReuseService) {
	a.runOrganizationWorkerWithWake(ctx, queue, svc, nil, reuse...)
}
