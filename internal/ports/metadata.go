package ports

import (
	"context"
	"time"
)

// MovieMetadata represents normalized movie or TV show information from a metadata provider.
type MovieMetadata struct {
	TMDBID        int64    `json:"tmdb_id"`
	MediaType     string   `json:"media_type"` // "movie" or "tv"
	IMDbID        string   `json:"imdb_id,omitempty"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Year          int      `json:"year,omitempty"`
	Overview      string   `json:"overview,omitempty"`
	PosterURL     string   `json:"poster_url,omitempty"`
	BackdropURL   string   `json:"backdrop_url,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Runtime       int      `json:"runtime,omitempty"`
	Rating        float64  `json:"rating,omitempty"`
	SeasonsCount  int      `json:"seasons_count,omitempty"`
	EpisodesCount int      `json:"episodes_count,omitempty"`
}

type TVSeasonDetails struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date,omitempty"`
	PosterURL    string `json:"poster_url,omitempty"`
}

type TVDetails struct {
	TMDBID        int64             `json:"tmdb_id"`
	Title         string            `json:"title"`
	OriginalTitle string            `json:"original_title,omitempty"`
	Year          int               `json:"year,omitempty"`
	Overview      string            `json:"overview,omitempty"`
	PosterURL     string            `json:"poster_url,omitempty"`
	BackdropURL   string            `json:"backdrop_url,omitempty"`
	Rating        float64           `json:"rating,omitempty"`
	Genres        []string          `json:"genres,omitempty"`
	Seasons       []TVSeasonDetails `json:"seasons"`
}

// MetadataProvider abstracts movie and TV metadata lookup.
type MetadataProvider interface {
	SearchMovies(ctx context.Context, query string, year int) ([]*MovieMetadata, error)
	SearchMedia(ctx context.Context, query, mediaType string, year int) ([]*MovieMetadata, error)
	GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*MovieMetadata, error)
	GetTVDetails(ctx context.Context, tmdbID int64, language string) (*TVDetails, error)
	FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*MovieMetadata, error)
	GetDiscoverFeed(ctx context.Context, category string, page int) ([]*MovieMetadata, error)
}

// MetadataRequestBudget owns admission across one provider's clients.
// Release is called only after the physical HTTP operation and body close.
type MetadataRequestBudget interface {
	Acquire(ctx context.Context) (release func(), err error)
	RecordRateLimit(retryAfter time.Duration)
}
