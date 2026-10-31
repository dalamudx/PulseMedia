package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

func (a *App) tvEpisodeUpgradeService(repository *downloadstore.Repository) (*acquisition.TVEpisodeUpgradeService, error) {
	native, err := downloadstore.NewTVEpisodeUpgradeRepositoryNative(repository, torznab.CompleteTVFetcher{}, torrent.Inspector{})
	if err != nil {
		return nil, err
	}
	files := rootfs.NewFilesystem()
	return acquisition.NewTVEpisodeUpgradeService(postgres.NewTVEpisodeUpgradeStore(a.dbPool, a.idgen, a.indexerDecoder, native, files, files, outbox.New(a.dbPool, a.idgen, a.clock))), nil
}
