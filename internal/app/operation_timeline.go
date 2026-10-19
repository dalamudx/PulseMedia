package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) OperationTimelineReader() ports.OperationTimelineReader {
	return postgres.NewOperationTimelineStore(a.dbPool)
}
