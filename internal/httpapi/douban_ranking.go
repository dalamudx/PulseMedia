package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/ranking"
)

type doubanRankingApplication interface {
	List(ctx context.Context, list string, page int) ([]ranking.Item, error)
}

func (s *ServerHandler) SetDoubanRankingService(service doubanRankingApplication) {
	s.doubanRankingSvc = service
}

func (s *ServerHandler) GetDoubanRanking(ctx context.Context, request GetDoubanRankingRequestObject) (GetDoubanRankingResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetDoubanRanking401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	if !metadataReadAllowed(actor) {
		return GetDoubanRanking403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "metadata read access denied", id)), nil
	}
	page := 1
	if request.Params.Page != nil {
		page = *request.Params.Page
	}
	if s.doubanRankingSvc == nil {
		return GetDoubanRanking503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "ranking temporarily unavailable", id)), nil
	}
	items, err := s.doubanRankingSvc.List(ctx, string(request.Params.List), page)
	if errors.Is(err, ports.ErrRejected) {
		return GetDoubanRanking400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_RANKING", "invalid ranking list or page", id)), nil
	}
	if err != nil || items == nil {
		return GetDoubanRanking503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "ranking temporarily unavailable", id)), nil
	}
	results := make([]DoubanRankingItem, 0, len(items))
	for _, item := range items {
		out := DoubanRankingItem{DoubanId: item.DoubanID, Title: item.Title, MediaType: DoubanRankingItemMediaType(item.MediaType)}
		if item.OriginalTitle != "" {
			value := item.OriginalTitle
			out.OriginalTitle = &value
		}
		if item.Year > 0 {
			value := item.Year
			out.Year = &value
		}
		if item.Rating > 0 {
			value := float32(item.Rating)
			out.Rating = &value
		}
		if item.PosterURL != "" {
			value := item.PosterURL
			out.PosterUrl = &value
		}
		if item.TMDBID > 0 {
			value := item.TMDBID
			out.TmdbId = &value
		}
		results = append(results, out)
	}
	return GetDoubanRanking200JSONResponse{Results: results}, nil
}
