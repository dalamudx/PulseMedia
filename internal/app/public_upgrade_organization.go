package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

func (a *App) publicUpgradeOrganizationService() (*acquisition.PublicUpgradeOrganizationService, error) {
	store, e := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{}, a.indexerDecoder)
	if e != nil {
		return nil, e
	}
	repo, e := postgres.NewPublicMovieUpgradeOrganizationStore(store, rootfs.NewFilesystem())
	if e != nil {
		return nil, e
	}
	return acquisition.NewPublicUpgradeOrganizationService(repo)
}
