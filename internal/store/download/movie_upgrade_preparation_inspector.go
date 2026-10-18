package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type movieUpgradePreparationInspector struct {
	masterKey []byte
	fetcher   ports.CompleteTVTorrentFetcher
	inspector ports.MovieUpgradeMetainfoInspector
}

func NewMovieUpgradePreparationInspector(key []byte, f ports.CompleteTVTorrentFetcher, i ports.MovieUpgradeMetainfoInspector) (ports.MovieUpgradePreparationInspector, error) {
	if len(key) != 32 || f == nil || i == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradePreparationInspector{append([]byte(nil), key...), f, i}, nil
}
func (e *movieUpgradePreparationInspector) InspectMovieUpgradePreparation(ctx context.Context, w *ports.MovieUpgradePreparationWork) (ports.MovieUpgradeMetainfoProof, error) {
	if w == nil {
		return nil, ports.ErrUnavailable
	}
	link, err := crypto.Decrypt(e.masterKey, w.DownloadURLEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer clear(link)
	key, err := crypto.Decrypt(e.masterKey, w.APIKeyEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer clear(key)
	raw, err := e.fetcher.FetchCompleteTVTorrent(ctx, ports.CompleteTVTorrentSource{EndpointURL: w.EndpointURL, APIKey: string(key), DownloadURL: string(link)})
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	return e.inspector.InspectMovieUpgradeMetainfo(ctx, raw, w.Target)
}
