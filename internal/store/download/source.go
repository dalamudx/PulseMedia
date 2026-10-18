package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func (r *Repository) SetTorrentSource(factory ports.DownloadTorrentSourceFactory, inspector ports.TorrentMetainfoInspector) {
	r.execution.SetTorrentSource(factory, inspector)
}

func (m *Manager) SetTorrentSource(factory ports.DownloadTorrentSourceFactory, inspector ports.TorrentMetainfoInspector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sourceFactory, m.inspector = factory, inspector
}

func (m *Manager) submissionPayload(ctx context.Context, candidate db.ReleaseCandidate, link string) (ports.AddTorrentParam, error) {
	var p ports.AddTorrentParam
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if strings.HasPrefix(link, "magnet:") {
		hash, err := ports.DownloadMagnetHash(link)
		if err != nil {
			return p, err
		}
		p.InfoHash, p.DownloadURL = hash, link
	} else {
		m.mu.RLock()
		factory, inspector, decoder := m.sourceFactory, m.inspector, m.indexerDecoder
		m.mu.RUnlock()
		if factory == nil || inspector == nil {
			return p, ports.ErrUnavailable
		}
		endpoint, err := m.queries.GetIndexerByID(ctx, candidate.IndexerID)
		if err != nil {
			return p, ports.ErrUnavailable
		}
		endpoint, modes, err := postgres.ResolveExecutableIndexerConfiguration(ctx, m.queries, decoder, endpoint)
		if err != nil {
			return p, ports.ErrUnavailable
		}
		if endpoint.Health != "online" || candidate.SourceConfigVersion != endpoint.ConfigVersion {
			return p, ports.ErrDownloadDemandChanged
		}
		credential, err := crypto.Decrypt(m.masterKey, endpoint.ApiKeyEncrypted)
		if err != nil {
			return p, ports.ErrUnavailable
		}
		source := factory(torrentSourceProtocol(endpoint, modes), endpoint.Url, string(credential))
		if source == nil {
			return p, ports.ErrUnavailable
		}
		raw, err := source.FetchTorrent(ctx, link)
		if err != nil {
			return p, err
		}
		if err = ctx.Err(); err != nil {
			return p, err
		}
		if len(raw) == 0 || len(raw) > ports.MaxTorrentMetainfoBytes {
			return p, ports.ErrInvalidTorrentMetainfo
		}
		metainfo, err := inspector.InspectTorrent(raw)
		if err != nil || metainfo == nil || !bytes.Equal(raw, metainfo.TorrentBytes) || metainfo.BytesSHA256 != sha256.Sum256(raw) {
			return p, ports.ErrInvalidTorrentMetainfo
		}
		p.InfoHash, p.TorrentBytes = metainfo.InfoHash, bytes.Clone(raw)
	}
	if candidate.InfoHash.Valid && candidate.InfoHash.String != "" && !strings.EqualFold(candidate.InfoHash.String, p.InfoHash) {
		return ports.AddTorrentParam{}, ports.ErrInvalidTorrentMetainfo
	}
	return p, nil
}
