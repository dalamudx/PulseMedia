package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) movieUpgradeObservationServices(downloads *downloadstore.Repository) (ports.MovieUpgradeObservationRuntime, ports.MovieUpgradeNativeJobObservationRepository, error) {
	preparation, err := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{}, a.indexerDecoder)
	if err != nil {
		return nil, nil, err
	}
	runtime, err := postgres.NewMovieUpgradeObservationRuntimeStore(preparation)
	if err != nil {
		return nil, nil, err
	}
	verifier := postgres.NewMovieUpgradeNativeQueryVerifier()
	driver, err := downloads.MovieUpgradeObservationDriver(verifier, a.clock)
	if err != nil {
		return nil, nil, err
	}
	repo, err := postgres.NewMovieUpgradeNativeJobObservationRepository(preparation, driver, verifier)
	if err != nil {
		return nil, nil, err
	}
	return runtime, repo, nil
}
