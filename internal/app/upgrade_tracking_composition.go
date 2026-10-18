package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) movieUpgradeTrackingServices(downloads *downloadstore.Repository) (ports.MovieUpgradeTrackingRuntime, ports.MovieUpgradeNativeJobTrackingRepository, error) {
	preparation, e := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{}, a.indexerDecoder)
	if e != nil {
		return nil, nil, e
	}
	runtime, e := postgres.NewMovieUpgradeTrackingRuntimeStore(preparation)
	if e != nil {
		return nil, nil, e
	}
	verifier := postgres.NewMovieUpgradeTrackingQueryVerifier()
	driver, e := downloads.MovieUpgradeTrackingDriver(verifier, torrent.Inspector{}, a.clock)
	if e != nil {
		return nil, nil, e
	}
	repo, e := postgres.NewMovieUpgradeNativeJobTrackingRepository(preparation, driver, verifier)
	if e != nil {
		return nil, nil, e
	}
	return runtime, repo, nil
}
