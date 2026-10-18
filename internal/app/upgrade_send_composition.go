package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) movieUpgradeSendServices(downloads *downloadstore.Repository) (ports.MovieUpgradeSendJobExecutor, error) {
	preparation, err := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{})
	if err != nil {
		return nil, err
	}
	work, err := postgres.NewMovieUpgradeSendWorkStore(preparation)
	if err != nil {
		return nil, err
	}
	verifier := postgres.NewMovieUpgradeNativeDispatchVerifier()
	driver, err := downloads.MovieUpgradeSendDriver(verifier, a.clock)
	if err != nil {
		return nil, err
	}
	native, err := postgres.NewMovieUpgradeNativeJobSendRepository(preparation, driver, verifier)
	if err != nil {
		return nil, err
	}
	return downloads.MovieUpgradeSendExecutor(work, torznab.CompleteTVFetcher{}, torrent.Inspector{}, native)
}
