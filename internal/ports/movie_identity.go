package ports

import "context"

// MovieIdentitySearch distinguishes a complete candidate set from a browse page.
// Complete must be false when pagination, counts, or source structure are unknown.
type MovieIdentitySearch struct {
	Candidates []*MovieMetadata
	Complete   bool
}

// MovieIdentityProvider supplies the evidence needed to identify a movie file.
type MovieIdentityProvider interface {
	SearchMovieIdentityCandidates(ctx context.Context, title string, year int) (MovieIdentitySearch, error)
	GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*MovieMetadata, error)
	FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*MovieMetadata, error)
}
