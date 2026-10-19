package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) LibraryOverviewReader() ports.LibraryOverviewReader {
	return postgres.NewLibraryOverviewStore(a.dbPool)
}

func (a *App) DashboardActivityReader() ports.DashboardActivityReader {
	return postgres.NewDashboardActivityStore(a.dbPool)
}
