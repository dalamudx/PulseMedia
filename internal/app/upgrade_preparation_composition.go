package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

func (a *App) publicUpgradePreparationService(masterKey []byte) (*acquisition.PublicUpgradePreparationService, error) {
	repo, err := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{})
	if err != nil {
		return nil, err
	}
	inspector, err := downloadstore.NewMovieUpgradePreparationInspector(masterKey, torznab.CompleteTVFetcher{}, torrent.Inspector{})
	if err != nil {
		return nil, err
	}
	return acquisition.NewPublicUpgradePreparationService(repo, inspector, a.idgen)
}
