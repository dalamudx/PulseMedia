package ports

import "context"

// Douban ranking list identifiers accepted by DoubanRankingProvider.
const (
	DoubanListHotMovie   = "hot_movie"
	DoubanListHotTV      = "hot_tv"
	DoubanListWeeklyBest = "weekly_best"
	DoubanListTop250     = "top250"
)

// DoubanRankingItem is validated provider data. It deliberately carries no
// TMDB identity; a later mapping step must establish that separately.
type DoubanRankingItem struct {
	DoubanID      string
	Title         string
	OriginalTitle string
	Year          int
	Rating        float64
	PosterURL     string
	MediaType     string // "movie" or "tv"
}

// DoubanRankingProvider reads one explicit ranking window. Failures use the
// shared external-error sentinels; callers must not retry inside the call.
type DoubanRankingProvider interface {
	GetRanking(ctx context.Context, list string, start, count int) ([]DoubanRankingItem, error)
}
