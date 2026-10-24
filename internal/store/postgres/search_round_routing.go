package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func freezeDownloadRoutes(ctx context.Context, q *db.Queries, rootPath, mediaType string, indexers []string, rules []endpoint.DownloadRouteRule) (map[string]ports.RoundDownloaderRoute, error) {
	clients, err := q.ListDownloadClients(ctx)
	if err != nil {
		return nil, err
	}
	available := make([]endpoint.DownloadRouteClient, 0, len(clients))
	for _, client := range clients {
		var mapping map[string]string
		eligible := (client.ClientType == "qbittorrent" || client.ClientType == "transmission" || (client.ClientType == "rtorrent" && mediaType == "movie")) && json.Unmarshal(client.RemotePathMap, &mapping) == nil && mapping != nil && naming.TranslateRemotePath(mapping, rootPath) != ""
		available = append(available, endpoint.DownloadRouteClient{ID: client.ID, Eligible: eligible})
	}
	result := make(map[string]ports.RoundDownloaderRoute, len(indexers))
	digests := make(map[uuid.UUID]string)
	for _, indexer := range indexers {
		site, err := uuid.Parse(indexer)
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		selected, err := endpoint.SelectDownloadRoute(endpoint.DownloadRouteRequest{MediaType: mediaType, IndexerID: site}, available, rules)
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		digest, exists := digests[selected.DownloaderID]
		if !exists {
			config, err := q.GetSearchDownloaderSnapshot(ctx, selected.DownloaderID)
			if err != nil {
				return nil, err
			}
			digest = config.ConfigDigest
			digests[selected.DownloaderID] = digest
		}
		result[indexer] = ports.RoundDownloaderRoute{DownloaderID: selected.DownloaderID, ConfigDigest: digest, PolicyVersion: selected.PolicyVersion, Basis: selected.Basis}
	}
	return result, nil
}
