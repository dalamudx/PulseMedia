package ports

import "context"

// MediaArtworkSession freezes configuration for one sidecar execution. Validate
// is a read-only check immediately before publishing optional artwork; it must
// not contact the external provider or authorize any file write itself.
type MediaArtworkSession interface {
	MediaArtworkProvider
	Validate(context.Context) error
}

type MediaArtworkSource interface {
	Open(context.Context) (MediaArtworkSession, error)
}
