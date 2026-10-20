package ports

import "context"

// MediaSidecarMetadata enriches an already bound identity; it never searches.
type MediaSidecarMetadata interface {
	GetMovieDetails(ctx context.Context, id int64, language string) (*MovieMetadata, error)
	GetTVDetails(ctx context.Context, id int64, language string) (*TVDetails, error)
}
