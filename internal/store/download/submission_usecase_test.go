package download_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	usecase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
)

// Store integration tests execute the production usecase, including physical budget ownership.
func submitDownload(m *download.Manager, ctx context.Context, op, client, candidate uuid.UUID, path string, lease ...ports.JobLease) (*ports.DownloadExecutionTask, error) {
	return usecase.SubmitDownload(ctx, m, op, client, candidate, path, lease...)
}
