package download

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

var (
	ErrNoDownloadClientAvailable = errors.New("no download client available")
)

type Router struct {
	queries *db.Queries
}

func NewRouter(queries *db.Queries) *Router {
	return &Router{queries: queries}
}

// SelectClient selects the most appropriate download client according to media type and availability.
func (r *Router) SelectClient(ctx context.Context, mediaType string, indexerID *uuid.UUID) (*db.DownloadClient, error) {
	clients, err := r.queries.ListDownloadClients(ctx)
	if err != nil || len(clients) == 0 {
		return nil, ErrNoDownloadClientAvailable
	}

	// 1. Specialized match: look for a client specialized for this media type
	targetKeyword := strings.ToLower(mediaType) // "movie" or "tv"
	for i := range clients {
		nameLower := strings.ToLower(clients[i].Name)
		if strings.Contains(nameLower, targetKeyword) {
			return &clients[i], nil
		}
	}

	// 2. Default fallback: return first active client
	return &clients[0], nil
}
