package ports

import "context"

// TVDBEpisodeResolution preserves both source identity and TMDB's canonical coordinate.
// A failed/empty cross-reference never authorizes an invented TMDB episode ID.
type TVDBEpisodeResolution struct {
	TMDBSeriesID  int64
	TVDBEpisodeID int64
	SeasonNumber  int
	Episode       TVEpisodeMetadata
}
type TVDBEpisodeResolver interface {
	ResolveTVDBEpisode(context.Context, int64, int64) (*TVDBEpisodeResolution, error)
}
