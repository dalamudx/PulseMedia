package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

// OperationReader assembles the readonly status boundary for HTTP and other readers.
func (a *App) OperationReader() ports.OperationQuery {
	return postgres.NewOperationQueryStore(a.dbPool)
}

// SubmissionRetryRepository assembles the original-job command boundary.
func (a *App) SubmissionRetryRepository() ports.SubmissionRetryRepository {
	return postgres.NewDownloadSubmissionStore(a.dbPool, a.idgen)
}
