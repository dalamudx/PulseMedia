package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

func tvRefreshFailure(ctx context.Context, e error) tvRefreshFailureResponse {
	return tvRefreshFailureResponse{failedControl(ctx, e)}
}

type tvRefreshSuccess struct {
	Body RefreshTVEpisodeMetadata200JSONResponse
}

func (s tvRefreshSuccess) VisitRefreshTVEpisodeMetadataResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(s.Body)
}
