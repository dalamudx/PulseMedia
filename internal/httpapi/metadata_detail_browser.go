package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type metadataDetailApplication interface {
	Credits(context.Context, ports.Actor, string, int64) (*ports.MetadataCredits, error)
	Season(context.Context, ports.Actor, int64, int) (*ports.MetadataSeasonDetails, error)
}

func (s *ServerHandler) SetMetadataDetailService(service metadataDetailApplication) {
	s.metadataDetailSvc = service
}

func metadataDetailProblem(err error, id string) (int, ErrorResponse) {
	status, code, message := http.StatusServiceUnavailable, "METADATA_UNAVAILABLE", "metadata is temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "metadata read access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid metadata request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "metadata not found"
	}
	return status, problemResponse(status, code, message, id)
}

func metadataOptionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *ServerHandler) GetTMDBMediaCredits(ctx context.Context, request GetTMDBMediaCreditsRequestObject) (GetTMDBMediaCreditsResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetTMDBMediaCredits401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	var result *ports.MetadataCredits
	err := ports.ErrUnavailable
	if s.metadataDetailSvc != nil {
		result, err = s.metadataDetailSvc.Credits(ctx, *actor, string(request.MediaType), request.Id)
	}
	if err != nil || result == nil {
		status, problem := metadataDetailProblem(err, id)
		switch status {
		case http.StatusForbidden:
			return GetTMDBMediaCredits403ApplicationProblemPlusJSONResponse(problem), nil
		case http.StatusBadRequest:
			return GetTMDBMediaCredits400ApplicationProblemPlusJSONResponse(problem), nil
		case http.StatusNotFound:
			return GetTMDBMediaCredits404ApplicationProblemPlusJSONResponse(problem), nil
		default:
			return GetTMDBMediaCredits503ApplicationProblemPlusJSONResponse(problem), nil
		}
	}
	cast := make([]TMDBCastMember, len(result.Cast))
	for i, member := range result.Cast {
		cast[i] = TMDBCastMember{TmdbId: member.TMDBID, Name: member.Name, Character: metadataOptionalText(member.Character), ProfileUrl: metadataOptionalText(member.ProfileURL)}
	}
	return GetTMDBMediaCredits200JSONResponse{TmdbId: result.TMDBID, MediaType: TMDBCreditsResponseMediaType(result.MediaType), Cast: cast}, nil
}

func (s *ServerHandler) GetTMDBSeasonDetails(ctx context.Context, request GetTMDBSeasonDetailsRequestObject) (GetTMDBSeasonDetailsResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetTMDBSeasonDetails401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	var result *ports.MetadataSeasonDetails
	err := ports.ErrUnavailable
	if s.metadataDetailSvc != nil {
		result, err = s.metadataDetailSvc.Season(ctx, *actor, request.Id, request.Season)
	}
	if err != nil || result == nil {
		status, problem := metadataDetailProblem(err, id)
		switch status {
		case http.StatusForbidden:
			return GetTMDBSeasonDetails403ApplicationProblemPlusJSONResponse(problem), nil
		case http.StatusBadRequest:
			return GetTMDBSeasonDetails400ApplicationProblemPlusJSONResponse(problem), nil
		case http.StatusNotFound:
			return GetTMDBSeasonDetails404ApplicationProblemPlusJSONResponse(problem), nil
		default:
			return GetTMDBSeasonDetails503ApplicationProblemPlusJSONResponse(problem), nil
		}
	}
	episodes := make([]TMDBEpisodeDetailsItem, len(result.Episodes))
	for i, episode := range result.Episodes {
		episodes[i] = TMDBEpisodeDetailsItem{TmdbId: episode.TMDBID, EpisodeNumber: episode.EpisodeNumber, Name: metadataOptionalText(episode.Name), Overview: metadataOptionalText(episode.Overview), AirDate: metadataOptionalText(episode.AirDate), StillUrl: metadataOptionalText(episode.StillURL)}
	}
	return GetTMDBSeasonDetails200JSONResponse{TmdbId: result.TMDBID, SeasonNumber: result.SeasonNumber, Complete: result.Complete, Episodes: episodes}, nil
}
