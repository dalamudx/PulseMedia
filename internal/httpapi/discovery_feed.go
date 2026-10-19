package httpapi

import (
	"context"
	"math"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *ServerHandler) GetDiscoverFeed(ctx context.Context, request GetDiscoverFeedRequestObject) (GetDiscoverFeedResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetDiscoverFeed401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	readable := metadataReadAllowed(actor)
	if !readable {
		return GetDiscoverFeed403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "metadata read access denied", id)), nil
	}
	category, page := "trending", 1
	if request.Params.Category != nil {
		category = string(*request.Params.Category)
	}
	if request.Params.Page != nil {
		page = *request.Params.Page
	}
	switch category {
	case "trending", "movie", "tv", "top_rated", "now_playing":
	default:
		return GetDiscoverFeed400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_CATEGORY", "invalid discover category", id)), nil
	}
	if page < 1 || page > 500 {
		return GetDiscoverFeed400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_PAGE", "invalid discover page", id)), nil
	}
	unavailable := func() (GetDiscoverFeedResponseObject, error) {
		return GetDiscoverFeed503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "metadata feed temporarily unavailable", id)), nil
	}
	if s.metaProvider == nil {
		return unavailable()
	}
	items, err := s.metaProvider.GetDiscoverFeed(ctx, category, page)
	if err != nil || items == nil {
		return unavailable()
	}
	results := make([]DiscoverMediaItem, 0, len(items))
	for _, item := range items {
		if item == nil || item.TMDBID <= 0 || item.Title == "" || (item.MediaType != "movie" && item.MediaType != "tv") || item.Year < 0 || item.Year > 9999 || math.IsNaN(item.Rating) || math.IsInf(item.Rating, 0) || item.Rating < 0 || item.Rating > 10 {
			return unavailable()
		}
		var rating *float32
		if item.Rating > 0 {
			value := float32(item.Rating)
			rating = &value
		}
		var year *int
		if item.Year > 0 {
			value := item.Year
			year = &value
		}
		results = append(results, DiscoverMediaItem{TmdbId: item.TMDBID, Title: item.Title, MediaType: DiscoverMediaItemMediaType(item.MediaType), Year: year, Overview: &item.Overview, PosterUrl: &item.PosterURL, BackdropUrl: &item.BackdropURL, Rating: rating, Genres: &item.Genres})
	}
	return GetDiscoverFeed200JSONResponse{Results: results}, nil
}

func metadataReadAllowed(actor *ports.Actor) bool {
	if actor == nil || actor.Type != ports.ActorTypeUser || (actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) {
		return false
	}
	if actor.Scopes == nil {
		return true
	}
	for _, scope := range actor.Scopes {
		if scope == "read" {
			return true
		}
	}
	return false
}
