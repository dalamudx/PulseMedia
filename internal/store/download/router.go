package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

var ErrNoDownloadClientAvailable = endpoint.ErrNoDownloadRouteClient

type Router struct {
	queries *db.Queries
	rules   []endpoint.DownloadRouteRule
}

func NewRouter(queries *db.Queries, rules ...endpoint.DownloadRouteRule) *Router {
	return &Router{queries: queries, rules: append([]endpoint.DownloadRouteRule(nil), rules...)}
}

// SelectClient is the legacy internal projection. Production authorization freezes
// the selected ID and configuration before any send; this method grants no permit.
func (r *Router) SelectClient(ctx context.Context, mediaType string, indexerID *uuid.UUID) (*db.DownloadClient, error) {
	clients, err := r.queries.ListDownloadClients(ctx)
	if err != nil {
		return nil, err
	}
	available := make([]endpoint.DownloadRouteClient, 0, len(clients))
	for _, client := range clients {
		available = append(available, endpoint.DownloadRouteClient{ID: client.ID, Eligible: true})
	}
	request := endpoint.DownloadRouteRequest{MediaType: mediaType}
	if indexerID != nil {
		request.IndexerID = *indexerID
	}
	selected, err := endpoint.SelectDownloadRoute(request, available, r.rules)
	if err != nil {
		return nil, err
	}
	for i := range clients {
		if clients[i].ID == selected.DownloaderID {
			return &clients[i], nil
		}
	}
	return nil, ErrNoDownloadClientAvailable
}
