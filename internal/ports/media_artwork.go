package ports

import (
	"context"
	"errors"
)

// ErrMetadataArtworkUnavailable safely represents failed or unsupported artwork.
var ErrMetadataArtworkUnavailable = errors.New("metadata artwork unavailable")

// MetadataArtworkFetcher accepts a provider opaque path, never an arbitrary URL.
// FetchPoster returns validated JPEG bytes suitable for poster.jpg.
type MetadataArtworkFetcher interface {
	FetchPoster(context.Context, string) ([]byte, error)
}
