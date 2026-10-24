package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
)

func (s *ServerHandler) PreviewNaming(ctx context.Context, request PreviewNamingRequestObject) (PreviewNamingResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return PreviewNaming401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return PreviewNaming403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "administrator session required", id)), nil
	}
	if request.Body == nil {
		return PreviewNaming400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid naming preview", id)), nil
	}
	if s.settingsSvc == nil {
		return PreviewNaming503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "naming settings unavailable", id)), nil
	}
	body := request.Body
	if body.ReleaseTitle != nil && len(*body.ReleaseTitle) > 1024 {
		return PreviewNaming400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "sample release title is too long", id)), nil
	}
	input := naming.MediaTemplateInput{Title: body.Title, Extension: body.Extension}
	if body.Year != nil {
		input.Year = *body.Year
	}
	if body.Season != nil {
		input.Season = *body.Season
	} else {
		input.Season = -1
	}
	if body.Episode != nil {
		input.Episode = *body.Episode
	}
	if body.TmdbId != nil {
		input.TMDBID = *body.TmdbId
	}
	if body.ImdbId != nil {
		input.IMDBID = *body.ImdbId
	}
	if body.ReleaseTitle != nil && *body.ReleaseTitle != "" {
		input.Quality = media.RecognizeRelease(*body.ReleaseTitle).Quality
	}
	preview := settings.NamingPreviewRequest{MediaType: string(body.MediaType), Input: input}
	if body.Templates != nil {
		preview.Rules = &naming.TemplateRules{MovieTemplate: body.Templates.MovieTemplate, EpisodeTemplate: body.Templates.EpisodeTemplate}
	}
	result, err := s.settingsSvc.PreviewNaming(ctx, actor, preview)
	if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) {
		return PreviewNaming403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "current administrator required", id)), nil
	}
	if errors.Is(err, ports.ErrInvalidSettingCommand) {
		return PreviewNaming400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid naming preview or missing sample field", id)), nil
	}
	if err != nil || result == nil {
		return PreviewNaming503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "naming settings unavailable", id)), nil
	}
	return PreviewNaming200JSONResponse(NamingPreviewResult{RelativePath: result.RelativePath, ConfigurationVersion: result.Version, Draft: result.Draft, ExecutionAllowed: false}), nil
}
