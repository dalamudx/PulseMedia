package ports

import (
	"context"
	"time"
)

type TVEpisodeMetadata struct {
	// TMDBEpisodeID is provider evidence; zero remains unknown for legacy/other providers.
	TMDBEpisodeID  int64
	EpisodeNumber  int
	AirDate        *time.Time
	AbsoluteNumber *int
}
type TVSeasonMetadata struct {
	SeasonNumber int
	Episodes     []TVEpisodeMetadata
	Complete     bool
}
type TVEpisodeProvider interface {
	GetTVSeasonMetadata(context.Context, int64, int, string) (*TVSeasonMetadata, error)
}
