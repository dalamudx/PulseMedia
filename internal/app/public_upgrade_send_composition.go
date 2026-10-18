package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

func (a *App) publicUpgradeSendService() (*acquisition.PublicUpgradeSendService, error) {
	preparation, err := postgres.NewUpgradePreparationStore(a.dbPool, a.idgen, torrent.Inspector{})
	if err != nil {
		return nil, err
	}
	commands, err := postgres.NewMovieUpgradeSendCommandRepository(preparation)
	if err != nil {
		return nil, err
	}
	return acquisition.NewPublicUpgradeSendService(commands)
}
