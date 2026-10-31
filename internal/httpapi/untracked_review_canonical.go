package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedReviewEpisodeApplication interface {
	EpisodeOptions(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, int64, int) ([]ports.UntrackedReviewEpisodeOption, error)
}

// Recognize this route before the old review guard rejects nonempty queries.
func untrackedReviewEpisodeOptionsPath(parts []string) bool {
	return len(parts) == 4 && parts[1] == "discoveries" && parts[3] == "episode-options"
}

// This guard rejects every method except GET, including default/HEAD fallthrough.
func validUntrackedReviewEpisodeOptionsTransport(r *http.Request, parts []string) bool {
	if r == nil || !untrackedReviewEpisodeOptionsPath(parts) || !canonicalUntrackedReviewID(parts[0]) || !canonicalUntrackedReviewID(parts[2]) || r.Method != http.MethodGet || r.URL.ForceQuery || len(r.Header.Values("Idempotency-Key")) != 0 {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 2 || len(query["tmdb_id"]) != 1 || len(query["season"]) != 1 {
		return false
	}
	id, err := strconv.ParseInt(query.Get("tmdb_id"), 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != query.Get("tmdb_id") {
		return false
	}
	season, err := strconv.Atoi(query.Get("season"))
	if err != nil || season < 1 || season > 99 || strconv.Itoa(season) != query.Get("season") {
		return false
	}
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			return false
		}
	}
	return true
}

func (s *ServerHandler) ReadUntrackedReviewEpisodeOptions(ctx context.Context, r ReadUntrackedReviewEpisodeOptionsRequestObject) (ReadUntrackedReviewEpisodeOptionsResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || r.Params.TmdbId < 1 || r.Params.Season < 1 || r.Params.Season > 99 {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	service, ok := s.untrackedReviewSvc.(untrackedReviewEpisodeApplication)
	if !ok {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	options, err := service.EpisodeOptions(ctx, actor, r.ScopeId, r.FileId, r.Params.TmdbId, r.Params.Season)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if options == nil || len(options) > 5000 {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	items := make([]UntrackedReviewEpisodeOption, 0, len(options))
	previous := 0
	identities := map[int64]bool{}
	units := map[string]bool{}
	for _, o := range options {
		proof := ports.UntrackedReviewInput{MediaType: "tv", TMDBID: r.Params.TmdbId, CanonicalUnitID: o.CanonicalUnitID, MediaMetadataVersion: o.MediaMetadataVersion, UnitMetadataVersion: o.UnitMetadataVersion, Season: o.Season, Episode: o.Episode, TMDBEpisodeID: o.TMDBEpisodeID}
		if !proof.CanonicalSelectionValid() || o.Season != r.Params.Season || o.Episode <= previous || o.UnitKey != media.FormatEpisodeUnitKey(o.Season, o.Episode) || o.Title == "" || identities[o.TMDBEpisodeID] || units[o.CanonicalUnitID] {
			return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
		}
		previous, identities[o.TMDBEpisodeID], units[o.CanonicalUnitID] = o.Episode, true, true
		items = append(items, UntrackedReviewEpisodeOption{CanonicalUnitId: o.CanonicalUnitID, MediaMetadataVersion: o.MediaMetadataVersion, UnitMetadataVersion: o.UnitMetadataVersion, Season: o.Season, Episode: o.Episode, TmdbEpisodeId: o.TMDBEpisodeID, UnitKey: o.UnitKey, Title: o.Title})
	}
	return ReadUntrackedReviewEpisodeOptions200JSONResponse{Items: items}, nil
}

func (r untrackedScopeErrorResponse) VisitReadUntrackedReviewEpisodeOptionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
