package download

import "github.com/dalamudx/PulseMedia/internal/ports"

// NewTVEpisodeUpgradeRepositoryNative reuses the original repository's Manager,
// including its frozen factory and per-client physical budget.
func NewTVEpisodeUpgradeRepositoryNative(repository *Repository, fetcher ports.CompleteTVTorrentFetcher, inspector ports.TorrentMetainfoInspector) (ports.TVEpisodeUpgradeNative, error) {
	if repository == nil {
		return nil, ports.ErrUnavailable
	}
	return NewTVEpisodeUpgradeNative(repository.execution, fetcher, inspector)
}