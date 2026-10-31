package download

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type tvEpisodeUpgradeNative struct {
	manager   *Manager
	fetcher   ports.CompleteTVTorrentFetcher
	inspector ports.TorrentMetainfoInspector
}

var _ ports.TVEpisodeUpgradeNative = (*tvEpisodeUpgradeNative)(nil)

func NewTVEpisodeUpgradeNative(manager *Manager, fetcher ports.CompleteTVTorrentFetcher, inspector ports.TorrentMetainfoInspector) (ports.TVEpisodeUpgradeNative, error) {
	if manager == nil || len(manager.masterKey) != 32 || fetcher == nil || inspector == nil {
		return nil, ports.ErrUnavailable
	}
	return &tvEpisodeUpgradeNative{manager: manager, fetcher: fetcher, inspector: inspector}, nil
}

func (n *tvEpisodeUpgradeNative) FetchTVEpisodeUpgrade(ctx context.Context, endpointURL string, downloadURLEncrypted, apiKeyEncrypted []byte) (*ports.AutomaticTVMetainfoProof, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	link, err := crypto.Decrypt(n.manager.masterKey, downloadURLEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer clear(link)
	key, err := crypto.Decrypt(n.manager.masterKey, apiKeyEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer clear(key)
	raw, err := n.fetcher.FetchCompleteTVTorrent(ctx, ports.CompleteTVTorrentSource{EndpointURL: endpointURL, DownloadURL: string(link), APIKey: string(key)})
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ports.InspectAutomaticTVMetainfo(n.inspector, raw)
}

type tvEpisodeUpgradeFiles interface {
	GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error)
}

type tvEpisodeUpgradeClient struct {
	ports.DownloadClient
	tvEpisodeUpgradeFiles
}

// Keep protocol state interpretation at the downloader boundary. The use case
// still requires exact identity, two observations and the complete manifest.
func (c *tvEpisodeUpgradeClient) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	rows, err := c.DownloadClient.ListTorrents(ctx, tag)
	if err != nil {
		return nil, err
	}
	out := make([]*ports.TorrentInfo, len(rows))
	for i, row := range rows {
		if row == nil {
			continue
		}
		value := *row
		value.State = "tracking"
		if completedObservation(row) && row.DownloadedBytes == row.TotalBytes {
			value.State = "completed"
		}
		out[i] = &value
	}
	return out, nil
}

func (n *tvEpisodeUpgradeNative) OpenTVEpisodeUpgradeClient(ctx context.Context, config ports.TVEpisodeUpgradeNativeConfig) (ports.TVEpisodeUpgradeClient, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if config.ClientID == uuid.Nil || config.ClientType == "" || config.URL == "" {
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	release, err := n.manager.acquireClientBudget(ctx, config.ClientID)
	if err != nil {
		return nil, nil, err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	n.manager.mu.RLock()
	factory := n.manager.factory
	n.manager.mu.RUnlock()
	if factory == nil {
		return nil, nil, ports.ErrUnavailable
	}
	password, err := crypto.Decrypt(n.manager.masterKey, config.PasswordEncrypted)
	if err != nil {
		return nil, nil, ports.ErrUnavailable
	}
	defer clear(password)
	// Construct from the authorized snapshot; clientFor would reload configuration.
	client, err := factory(config.ClientType, config.URL, config.Username, string(password))
	if err != nil || client == nil {
		return nil, nil, ports.ErrUnavailable
	}
	var native ports.DownloadClient
	switch capable := client.(type) {
	case interface{ AutomaticTVByteSender() ports.DownloadClient }:
		native = capable.AutomaticTVByteSender()
	case interface{ MovieUpgradeByteSender() ports.DownloadClient }:
		native = capable.MovieUpgradeByteSender()
	default:
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if native == nil {
		return nil, nil, ports.ErrUnavailable
	}
	files, ok := native.(tvEpisodeUpgradeFiles)
	if !ok {
		return nil, nil, ports.ErrTVEpisodeUpgradeConflict
	}
	// The caller retains admission until both physical I/O and writeback return.
	keep = true
	return &tvEpisodeUpgradeClient{DownloadClient: native, tvEpisodeUpgradeFiles: files}, release, nil
}
