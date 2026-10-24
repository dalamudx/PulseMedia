package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) DownloadGlobalRatesOverviewReader() ports.DownloadGlobalRatesOverviewReader {
	return postgres.NewDownloadGlobalRatesOverviewStore(a.dbPool)
}
func (a *App) DownloadGlobalRatesCollectionEnabled() bool { return a.cfg.DownloadGlobalRates.Enabled }
